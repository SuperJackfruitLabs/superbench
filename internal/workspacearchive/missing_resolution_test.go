package workspacearchive_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacearchive"
)

func ambiguousRootRemoval(t *testing.T) (*missingFixture, *workspacearchive.Plan) {
	t.Helper()
	f := makeMissing(t, false)
	_, e := f.m.Archive(context.Background(), "source", 0, nil)
	must(t, e)
	p, e := f.m.PlanReap(context.Background(), "source")
	must(t, e)
	save := f.m.SaveIntent
	f.m.SaveIntent = func(path string, raw []byte) error {
		if e := save(path, raw); e != nil {
			return e
		}
		if strings.HasPrefix(filepath.Base(path), "aggregate-root-removal-") {
			return errors.New("crash after durable removal intent, before unlink")
		}
		return nil
	}
	if _, e = f.m.Reap(context.Background(), *p); e == nil {
		t.Fatal("pre-unlink fault did not fire")
	}
	f.m.SaveIntent = save
	if _, e = f.m.Reap(context.Background(), *p); e == nil {
		t.Fatal("ordinary reap adopted ambiguous present root")
	}
	return f, p
}

func TestExplicitCurrentRootDispositionPreservesOriginalIntentAndCompletesReap(t *testing.T) {
	f, p := ambiguousRootRemoval(t)
	ctx := context.Background()
	review, e := f.m.InspectRootRemoval(ctx, "source", "review-one")
	must(t, e)
	if review.Action != "remove-current-empty-directory" || review.Root.Absent {
		t.Fatal("review claimed original instance or absence")
	}
	raw, e := review.Bytes()
	must(t, e)
	parsed, e := workspacearchive.ParseRootRemovalReview(raw)
	must(t, e)
	for _, bad := range [][]byte{append(append([]byte{}, raw...), []byte("{}")...), append([]byte(`{"unknown":true,`), raw[1:]...), bytes.Replace(raw, []byte(`"ID":"review-one"`), []byte(`"ID":"review-one","ID":"review-one"`), 1)} {
		if _, e = workspacearchive.ParseRootRemovalReview(bad); e == nil {
			t.Fatal("ambiguous disposition JSON accepted")
		}
	}
	original := filepath.Join(f.s.State, "aggregate-root-removal-source.json")
	before, e := os.ReadFile(original)
	must(t, e)

	must(t, os.WriteFile(original, append(append([]byte{}, before...), ' '), 0600))
	if e = f.m.ReconcileRootRemoval(ctx, *parsed); e == nil {
		t.Fatal("changed original removal authority accepted")
	}
	must(t, os.WriteFile(original, before, 0600))
	parent := filepath.Dir(f.b.Path)
	parentInfo, e := os.Stat(parent)
	must(t, e)
	must(t, os.Chmod(parent, parentInfo.Mode().Perm()^0020))
	if _, e = f.m.InspectRootRemoval(ctx, "source", "changed-parent"); e == nil {
		t.Fatal("fresh review adopted changed fixed parent")
	}
	must(t, os.Chmod(parent, parentInfo.Mode().Perm()))
	sentinel := filepath.Join(f.b.Path, "new-file")
	must(t, os.WriteFile(sentinel, []byte("preserve new work"), 0600))
	if e = f.m.ReconcileRootRemoval(ctx, *parsed); e == nil {
		t.Fatal("nonempty current path disposed")
	}
	actual, e := os.ReadFile(sentinel)
	must(t, e)
	if string(actual) != "preserve new work" {
		t.Fatal("new work changed")
	}
	must(t, os.Remove(sentinel))
	savedRoot := f.b.Path + "-owned-evidence"
	must(t, os.Rename(f.b.Path, savedRoot))
	must(t, os.Symlink(filepath.Join(f.s.Data, "benches", "target"), f.b.Path))
	if e = f.m.ReconcileRootRemoval(ctx, *parsed); e == nil {
		t.Fatal("symlink current path disposed")
	}
	if st, e := os.Lstat(f.b.Path); e != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("unreviewed symlink changed", e)
	}
	must(t, os.Remove(f.b.Path))
	must(t, os.Rename(savedRoot, f.b.Path))
	priorInventory, e := f.s.Snapshot("source")
	must(t, e)
	priorBytes, e := json.Marshal(priorInventory)
	must(t, e)
	must(t, f.m.ReconcileRootRemoval(ctx, *parsed))
	if _, e = os.Lstat(f.b.Path); !os.IsNotExist(e) {
		t.Fatal("reviewed empty root remains", e)
	}
	actual, e = os.ReadFile(original)
	must(t, e)
	if !bytes.Equal(before, actual) {
		t.Fatal("original root-removal authority was overwritten")
	}
	// Current-path disposition never completes quota/state/history retirement.
	inventory, e := f.s.Snapshot("source")
	must(t, e)
	afterBytes, e := json.Marshal(inventory)
	must(t, e)
	if !bytes.Equal(priorBytes, afterBytes) {
		t.Fatal("disposition changed source state")
	}
	if inventory[0].Workspace.Phase != "reaping" {
		t.Fatal("disposition fabricated completed reap")
	}
	must(t, os.Mkdir(f.b.Path, 0700))
	if e = f.m.ReconcileRootRemoval(ctx, *parsed); e == nil {
		t.Fatal("same attempt deleted a returned empty root")
	}
	if _, e = os.Stat(f.b.Path); e != nil {
		t.Fatal("returned root lost", e)
	}
	next, e := f.m.InspectRootRemoval(ctx, "source", "review-two")
	must(t, e)
	must(t, f.m.ReconcileRootRemoval(ctx, *next))
	_, e = f.m.Reap(ctx, *p)
	must(t, e)
	must(t, f.m.ReconcileRootRemoval(ctx, *next)) // cap-free exact absence replay
}

func TestRootDispositionJournalingFaultRequiresFreshReviewAndPreservesLateFiles(t *testing.T) {
	f, p := ambiguousRootRemoval(t)
	ctx := context.Background()
	review, e := f.m.InspectRootRemoval(ctx, "source", "attempt-one")
	must(t, e)
	save := f.m.SaveIntent
	f.m.SaveIntent = func(path string, raw []byte) error {
		if e := save(path, raw); e != nil {
			return e
		}
		return errors.New("crash after disposition intent, before unlink")
	}
	if e = f.m.ReconcileRootRemoval(ctx, *review); e == nil {
		t.Fatal("disposition journal fault did not fire")
	}
	f.m.SaveIntent = save
	if e = f.m.ReconcileRootRemoval(ctx, *review); e == nil {
		t.Fatal("ambiguous present-root attempt automatically retried")
	}
	if _, e = f.m.InspectRootRemoval(ctx, "source", "attempt-one"); e == nil {
		t.Fatal("used attempt ID was inspected again")
	}
	next, e := f.m.InspectRootRemoval(ctx, "source", "attempt-two")
	must(t, e)
	sentinel := filepath.Join(f.b.Path, "late-file")
	f.m.SaveIntent = func(path string, raw []byte) error {
		if e := save(path, raw); e != nil {
			return e
		}
		return os.WriteFile(sentinel, []byte("retain late file"), 0600)
	}
	if e = f.m.ReconcileRootRemoval(ctx, *next); e == nil {
		t.Fatal("late file escaped post-journal observation")
	}
	actual, e := os.ReadFile(sentinel)
	must(t, e)
	if string(actual) != "retain late file" {
		t.Fatal("late file changed")
	}
	// Move only the test-created file to owned evidence; never clean unknown work.
	must(t, os.Rename(sentinel, filepath.Join(filepath.Dir(f.s.State), "late-file-evidence")))
	f.m.SaveIntent = save
	if e = f.m.ReconcileRootRemoval(ctx, *next); e == nil {
		t.Fatal("same present-root attempt revived after late-file failure")
	}
	final, e := f.m.InspectRootRemoval(ctx, "source", "attempt-three")
	must(t, e)
	must(t, f.m.ReconcileRootRemoval(ctx, *final))
	_, e = f.m.Reap(ctx, *p)
	must(t, e)
}

package workspacerecreatetest

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// A create whose Fresh bootstrap failed keeps its materializing record. A
// different create on the key asks root, and replaces the record with its
// next generation only on root's proof that the allocation was retired.
func TestACreateOnAFailedCreateWaitsForRootsRetirement(t *testing.T) {
	f := newFixture(t)
	fresh := f.m.Fresh
	f.m.Fresh = func(ctx context.Context, b *model.Bench) error {
		if len(f.fresh) == 0 {
			f.fresh = append(f.fresh, workspace.Clone(b))
			return errors.New("bootstrap failed while materializing")
		}
		return fresh(ctx, b)
	}
	if _, err := f.m.Stage(context.Background(), "request1", f.spec); err == nil {
		t.Fatal("the failed create succeeded")
	}
	old := f.read(t)
	if old.Workspace.Phase != "materializing" || old.ProjectID != 0 || !workspacepolicy.Unfinished(old) {
		t.Fatalf("failed record: %+v", old.Workspace)
	}
	var asked []*model.Bench
	proven := map[string]bool{}
	f.m.Retired = func(_ context.Context, b *model.Bench) error {
		asked = append(asked, workspace.Clone(b))
		if !proven[b.Workspace.ID] {
			return errors.New("root: the allocation is not retired")
		}
		return nil
	}
	refused := func(name string, m workspace.Manager, request string) {
		t.Helper()
		_, err := m.Stage(context.Background(), request, f.spec)
		if err == nil || name != "legacy" && (!errors.Is(err, workspacepolicy.ErrUnretired) || !strings.Contains(err.Error(), "conflicts with an existing bench")) {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(f.read(t), old) || len(f.fresh) != 1 {
			t.Fatalf("%s changed the record or reached the helper", name)
		}
	}
	// Without a proof route, and while root has not retired it, the record stands.
	noProof := f.m
	noProof.Retired = nil
	refused("no proof route", noProof, "request2")
	refused("not retired", f.m, "request2")
	if len(asked) != 1 || !reflect.DeepEqual(asked[0], old) {
		t.Fatalf("root was not asked about the exact record: %d", len(asked))
	}
	legacy := f.m
	legacy.Fresh = nil
	refused("legacy", legacy, "request2")
	t.Log("FAILED_CREATE_DAEMON_REFUSED_UNTIL_RETIRED_PASS")

	// Root retired it: the next create makes the next generation.
	proven[old.Workspace.ID] = true
	second, err := f.m.Stage(context.Background(), "request2", f.spec)
	if err != nil {
		t.Fatal("next generation:", err)
	}
	w := second.Workspace
	if w.Phase != "initializing" || w.Generation != 2 || w.Previous != old.Workspace.ID || w.ID == old.Workspace.ID || w.CreationID != "request2" || second.ProjectID != 10000 {
		t.Fatalf("second generation: %+v %d", w, second.ProjectID)
	}
	if got := f.fresh[1]; len(f.fresh) != 2 || got.ProjectID != 0 || got.Workspace.Generation != 2 || got.Workspace.Previous != old.Workspace.ID || got.Workspace.ID != w.ID {
		t.Fatalf("helper input %+v", got.Workspace)
	}
	t.Log("FAILED_CREATE_DAEMON_NEXT_GENERATION_PASS")
	// The same create retries exactly. The second generation is initializing
	// with a live owner: any other create conflicts without asking root.
	asked = nil
	if retry, err := f.m.Stage(context.Background(), "request2", f.spec); err != nil || !reflect.DeepEqual(retry, second) || len(f.fresh) != 2 {
		t.Fatalf("retry: %v %d", err, len(f.fresh))
	}
	for _, request := range []string{"request1", "request3"} {
		if _, err := f.m.Stage(context.Background(), request, f.spec); err == nil || !strings.Contains(err.Error(), "conflicts with an existing bench") {
			t.Errorf("%s on the second generation: %v", request, err)
		}
	}
	if !reflect.DeepEqual(f.read(t), second) || len(f.fresh) != 2 || len(asked) != 0 {
		t.Fatalf("a refused create changed the second generation; root asked %d times", len(asked))
	}
}

// setRecord rewrites the key's record directly, as another daemon step would.
func (f *fixture) setRecord(t *testing.T, b *model.Bench) {
	t.Helper()
	record, _ := json.Marshal(b)
	db, err := statestore.Open(filepath.Join(f.m.State, "bench.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("UPDATE benches SET record=? WHERE key='card'", string(record)); err != nil {
		t.Fatal(err)
	}
}

// A bench whose bootstrap completed but whose initialization stalled gives
// way to a new create only once the daemon stopped its initialization owner,
// and then only on root's reclaim of it; a record that changes while the new
// create selects its source is never replaced.
func TestACreateOnAStalledCreateFollowsRootsReclaim(t *testing.T) {
	f := newFixture(t)
	first, err := f.m.Stage(context.Background(), "request1", f.spec)
	if err != nil || first.Workspace.Phase != "initializing" || first.ProjectID == 0 || workspacepolicy.Stalled(first) {
		t.Fatalf("an initializing first generation: %+v %v", first, err)
	}
	var asked []*model.Bench
	reclaimed := false
	f.m.Retired = func(_ context.Context, b *model.Bench) error {
		asked = append(asked, workspace.Clone(b))
		if !reclaimed {
			return errors.New("root: the bench is in use")
		}
		return nil
	}
	// A live initialization owner: the create conflicts and root is not asked.
	if _, err = f.m.Stage(context.Background(), "request2", f.spec); err == nil || len(asked) != 0 || !reflect.DeepEqual(f.read(t), first) {
		t.Fatalf("a create over a live owner: %v %d", err, len(asked))
	}
	// The daemon stopped the owner: root is asked, and refuses until it reclaims.
	stalled := workspace.Clone(first)
	stalled.Lease.StopRequested, stalled.Lease.Continuation = true, ""
	f.setRecord(t, stalled)
	if _, err = f.m.Stage(context.Background(), "request2", f.spec); !errors.Is(err, workspacepolicy.ErrUnretired) || len(asked) != 1 || !reflect.DeepEqual(asked[0], stalled) || !reflect.DeepEqual(f.read(t), stalled) {
		t.Fatalf("a create before root's reclaim: %v %d", err, len(asked))
	}
	t.Log("STUCK_CREATE_DAEMON_STALLED_REFUSED_UNTIL_RECLAIMED_PASS")
	// The record changes after root's answer, before the new record is
	// committed: the create refuses and replaces nothing.
	reclaimed = true
	changed := workspace.Clone(stalled)
	changed.Lease.Token = strings.Repeat("9", 48)
	retired := f.m.Retired
	f.m.Retired = func(ctx context.Context, b *model.Bench) error {
		f.setRecord(t, changed)
		return retired(ctx, b)
	}
	if _, err = f.m.Stage(context.Background(), "request2", f.spec); err == nil || !strings.Contains(err.Error(), "appeared during source selection") || !reflect.DeepEqual(f.read(t), changed) || len(f.fresh) != 1 {
		t.Fatalf("a create over a changed record: %v", err)
	}
	t.Log("STUCK_CREATE_DAEMON_CHANGED_RECORD_NOT_REPLACED_PASS")
	f.m.Retired = retired
	second, err := f.m.Stage(context.Background(), "request2", f.spec)
	if err != nil || second.Workspace.Generation != 2 || second.Workspace.Previous != first.Workspace.ID || second.ProjectID == first.ProjectID || len(f.fresh) != 2 {
		t.Fatalf("next generation after root's reclaim: %+v %v", second, err)
	}
	t.Log("STUCK_CREATE_DAEMON_NEXT_GENERATION_AFTER_RECLAIM_PASS")
}

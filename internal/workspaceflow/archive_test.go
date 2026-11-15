package workspaceflow

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func archiveFixture(t *testing.T) (bench.Store, workspace.Spec, *model.Bench) {
	t.Helper()
	store, spec, _ := fixture(t)
	t.Cleanup(func() {
		// Only thaw this test's disposable tree, after every assertion has finished.
		_ = filepath.WalkDir(store.Data, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink == 0 {
				return os.Chmod(path, 0700)
			}
			return nil
		})
	})
	for _, member := range spec.Repositories {
		if err := os.WriteFile(filepath.Join(member.Repository, ".gitignore"), []byte("/output/\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, member.Repository, "add", ".")
		git(t, member.Repository, "commit", "-m", "ignore private output")
	}
	ready, err := store.InitializeWorkspaceContext(context.Background(), "archive-create", spec)
	if err != nil {
		t.Fatal(err)
	}
	return store, spec, ready
}

func assertReadOnly(t *testing.T, root string) {
	t.Helper()
	for path, entry := range tree(t, root) {
		if entry.Mode&os.ModeSymlink == 0 && entry.Mode.Perm()&0222 != 0 {
			t.Fatal("archive entry remains writable", path, entry.Mode)
		}
	}
}

func recoveryOf(source *model.Bench, key string) workspace.RecoverySpec {
	spec := workspace.RecoverySpec{Key: key, Source: workspace.RecoverySource{BenchID: source.Workspace.ID, Commit: source.Workspace.Checkpoint.Seal, ManifestSHA256: source.Workspace.Checkpoint.Digest}}
	for _, member := range source.Workspace.Members {
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: member.Name, Repository: member.Repository})
		if member.Name == source.Workspace.CheckpointRepository {
			spec.Source.Repository = member.Repository
		}
	}
	return spec
}

func TestWorkspaceArchiveReviewedReapPreservesOtherWorkAndRemoteRecovery(t *testing.T) {
	ctx := context.Background()
	store, spec, ready := archiveFixture(t)
	otherSpec := spec
	otherSpec.Key = "independent"
	other, err := store.InitializeWorkspaceContext(ctx, "independent-create", otherSpec)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range ready.Workspace.Members {
		if err := os.Mkdir(filepath.Join(member.Path, "output"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(member.Path, "output", "private"), []byte("not in Git"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	checks := 0
	archived, err := store.ArchiveWorkspaceContext(ctx, ready.Key, 0, func(_ context.Context, b *bench.Bench) error {
		checks++
		if len(b.Workspace.Members) != 2 || b.Lease != nil {
			t.Fatal("archive check lost whole workspace")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Workspace.Phase != "archived" || archived.Workspace.Archive == nil || archived.Workspace.Archive.FrozenAt == "" || archived.Lease != nil || checks != 1 {
		t.Fatal("archive did not complete whole read-only transition")
	}
	assertReadOnly(t, ready.Path)
	again, err := store.ArchiveWorkspaceContext(ctx, ready.Key, time.Hour, func(context.Context, *bench.Bench) error {
		t.Fatal("archive retry repeated admission check")
		return nil
	})
	if err != nil || !reflect.DeepEqual(archived, again) {
		t.Fatal("archive retry changed retained intent", err)
	}
	before := tree(t, ready.Path)
	if replay, err := store.InitializeWorkspaceContext(ctx, "archive-create", spec); err != nil || !reflect.DeepEqual(archived, replay) || !reflect.DeepEqual(before, tree(t, ready.Path)) {
		t.Fatal("creation replay recreated archived checkout", err)
	}
	if _, err := store.AcquireWorkspaceContext(ctx, ready.Key, "writer"); err == nil {
		t.Fatal("archived workspace acquired")
	}
	if _, err := store.PrepareWorkspaceContext(ctx, ready.Key, strings.Repeat("a", 32), "", false); err == nil {
		t.Fatal("archived workspace prepared")
	}
	if _, err := store.RunWorkspaceContext(ctx, ready.Key, strings.Repeat("a", 32), "", []string{"true"}, false); err == nil {
		t.Fatal("archived workspace executed")
	}
	if _, err := store.CheckpointWorkspaceContext(ctx, ready.Key, strings.Repeat("a", 32)); err == nil {
		t.Fatal("archived workspace checkpointed")
	}
	if !reflect.DeepEqual(before, tree(t, ready.Path)) {
		t.Fatal("refused operation changed frozen files")
	}
	fromArchived, err := store.RecoverWorkspaceContext(ctx, "archive-recovery", recoveryOf(ready, "from-archive"))
	if err != nil {
		t.Fatal("archived seal recovery failed", err)
	}
	plan, err := store.PlanWorkspaceReapContext(ctx, ready.Key)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Members) != 2 || plan.BenchID != ready.Workspace.ID || !reflect.DeepEqual(plan.Checkpoint, ready.Workspace.Checkpoint) {
		t.Fatal("review does not bind complete sealed member vector")
	}
	entries := map[string]bool{}
	for _, entry := range plan.Tree.Entries {
		entries[entry.Path] = entry.Recoverable
	}
	for _, member := range ready.Workspace.Members {
		if recoverable, ok := entries[filepath.Join(member.Name, ".git")]; !ok || recoverable {
			t.Fatal("worktree pointer missing or marked recoverable")
		}
		if recoverable, ok := entries[filepath.Join(member.Name, "output", "private")]; !ok || recoverable {
			t.Fatal("private ignored output missing from deletion approval")
		}
		if !entries[filepath.Join(member.Name, "source.txt")] {
			t.Fatal("tracked sealed source not marked recoverable")
		}
	}
	remoteRefs := map[string]string{}
	for _, member := range ready.Workspace.Members {
		remoteRefs[member.Repository] = git(t, member.Repository, "show-ref")
	}
	otherFiles, recoveredFiles := tree(t, other.Path), tree(t, fromArchived.Path)
	canonicalRoot, err := filepath.EvalSymlinks(ready.Path)
	if err != nil {
		t.Fatal(err)
	}
	canonicalOther, err := filepath.EvalSymlinks(other.Path)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRecovered, err := filepath.EvalSymlinks(fromArchived.Path)
	if err != nil {
		t.Fatal(err)
	}
	reaped, err := store.ReapWorkspaceContext(ctx, *plan)
	if err != nil {
		t.Fatal(err)
	}
	if reaped.Workspace.Phase != "reaped" || reaped.Workspace.Archive.ReapedAt == "" {
		t.Fatal("reap completion not retained")
	}
	if _, err := os.Lstat(ready.Path); !os.IsNotExist(err) {
		t.Fatal("reviewed root remains", err)
	}
	for _, member := range ready.Workspace.Members {
		registrations := git(t, member.Mirror, "worktree", "list", "--porcelain", "-z")
		if strings.Contains(registrations, "worktree "+filepath.Join(canonicalRoot, member.Name)+"\x00") {
			t.Fatal("reaped member registration remains")
		}
		if !strings.Contains(registrations, "worktree "+filepath.Join(canonicalOther, member.Name)+"\x00") || !strings.Contains(registrations, "worktree "+filepath.Join(canonicalRecovered, member.Name)+"\x00") {
			t.Fatal("reap removed unrelated worktree registration")
		}
		if git(t, member.Repository, "show-ref") != remoteRefs[member.Repository] {
			t.Fatal("reap changed remote seal, pins, or task branches")
		}
	}
	if !reflect.DeepEqual(otherFiles, tree(t, other.Path)) || !reflect.DeepEqual(recoveredFiles, tree(t, fromArchived.Path)) {
		t.Fatal("reap changed independent workspace files")
	}
	replay, err := store.ReapWorkspaceContext(ctx, *plan)
	if err != nil || !reflect.DeepEqual(reaped, replay) {
		t.Fatal("reap receipt did not replay", err)
	}
	replay, err = store.InitializeWorkspaceContext(ctx, "archive-create", spec)
	if err != nil || !reflect.DeepEqual(reaped, replay) {
		t.Fatal("creation replay recreated retired checkout", err)
	}
	if _, err := os.Lstat(ready.Path); !os.IsNotExist(err) {
		t.Fatal("creation replay recreated root")
	}
	recovered, err := store.RecoverWorkspaceContext(ctx, "reaped-recovery", recoveryOf(ready, "from-reaped"))
	if err != nil {
		t.Fatal("reaped seal recovery failed", err)
	}
	for i, member := range recovered.Workspace.Members {
		if git(t, member.Path, "rev-parse", "HEAD") != ready.Workspace.Members[i].Source {
			t.Fatal("reaped recovery changed source vector")
		}
	}
}

func TestWorkspaceArchiveRefusesOwnerDirtyMemberAndMissingPin(t *testing.T) {
	for _, name := range []string{"owner", "dirty later member", "locked later registration", "missing pin"} {
		t.Run(name, func(t *testing.T) {
			store, _, ready := archiveFixture(t)
			later := ready.Workspace.Members[1]
			switch name {
			case "owner":
				if _, err := store.AcquireWorkspaceContext(context.Background(), ready.Key, "held"); err != nil {
					t.Fatal(err)
				}
			case "dirty later member":
				if err := os.WriteFile(filepath.Join(later.Path, "source.txt"), []byte("preserve dirty work"), 0600); err != nil {
					t.Fatal(err)
				}
			case "locked later registration":
				git(t, later.Mirror, "worktree", "lock", later.Path)
			case "missing pin":
				pin, err := checkpointset.PinRef(ready.Workspace.ID, ready.Workspace.Checkpoint.GenerationID, later.Name)
				if err != nil {
					t.Fatal(err)
				}
				git(t, later.Repository, "update-ref", "-d", pin)
			}
			before := snapshotWorkspace(t, store, ready.Key)
			beforeFiles := tree(t, ready.Path)
			if _, err := store.ArchiveWorkspaceContext(context.Background(), ready.Key, 0, nil); err == nil {
				t.Fatal("unsafe aggregate archive accepted")
			}
			if !reflect.DeepEqual(before, snapshotWorkspace(t, store, ready.Key)) || !reflect.DeepEqual(beforeFiles, tree(t, ready.Path)) {
				t.Fatal("failed archive changed ownership, files, or permissions")
			}
		})
	}
}

func TestWorkspaceReapRefusesGraceAndChangedReviewedFiles(t *testing.T) {
	for _, name := range []string{"grace", "changed bytes"} {
		t.Run(name, func(t *testing.T) {
			store, _, ready := archiveFixture(t)
			grace := time.Duration(0)
			if name == "grace" {
				grace = time.Hour
			}
			if _, err := store.ArchiveWorkspaceContext(context.Background(), ready.Key, grace, nil); err != nil {
				t.Fatal(err)
			}
			plan, err := store.PlanWorkspaceReapContext(context.Background(), ready.Key)
			if err != nil {
				t.Fatal(err)
			}
			if name == "changed bytes" {
				path := filepath.Join(ready.Workspace.Members[1].Path, "source.txt")
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("changed after approval"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := tree(t, ready.Path)
			if _, err := store.ReapWorkspaceContext(context.Background(), *plan); err == nil {
				t.Fatal("unsafe reviewed reap accepted")
			}
			if !reflect.DeepEqual(before, tree(t, ready.Path)) {
				t.Fatal("refused reap removed or changed retained files")
			}
		})
	}
}

// Manual archive preserves hidden or transformed bytes. Only explicit review
// of the raw manifest can approve deleting content that the seal cannot restore.
func TestWorkspaceArchiveRetainsIndexHiddenBytesAsNonrecoverable(t *testing.T) {
	store, _, ready := archiveFixture(t)
	member := ready.Workspace.Members[1]
	git(t, member.Path, "update-index", "--assume-unchanged", "source.txt")
	path := filepath.Join(member.Path, "source.txt")
	raw := []byte("private bytes hidden from Git status")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ArchiveWorkspaceContext(context.Background(), ready.Key, 0, nil); err != nil {
		t.Fatal(err)
	}
	assertReadOnly(t, ready.Path)
	if retained, err := os.ReadFile(path); err != nil || string(retained) != string(raw) {
		t.Fatal("manual archive discarded hidden physical bytes", err)
	}
	plan, err := store.PlanWorkspaceReapContext(context.Background(), ready.Key)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range plan.Tree.Entries {
		if entry.Path == filepath.Join(member.Name, "source.txt") {
			if entry.Recoverable {
				t.Fatal("index flag mislabeled private bytes as recoverable")
			}
			return
		}
	}
	t.Fatal("raw review omitted hidden tracked bytes")
}

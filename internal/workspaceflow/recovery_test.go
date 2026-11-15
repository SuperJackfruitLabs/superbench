package workspaceflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func recoveryFixture(t *testing.T) (bench.Store, workspace.RecoverySpec, *model.Bench) {
	t.Helper()
	store, spec, _ := fixture(t)
	source, err := store.InitializeWorkspaceContext(context.Background(), "source-create", spec)
	if err != nil {
		t.Fatal(err)
	}
	cp := source.Workspace.Checkpoint
	recovery := workspace.RecoverySpec{Key: "recovered", Repositories: append([]workspace.Repository(nil), spec.Repositories...), Source: workspace.RecoverySource{Repository: spec.Repositories[0].Repository, BenchID: source.Workspace.ID, Commit: cp.Seal, ManifestSHA256: cp.Digest}}
	return store, recovery, source
}

func snapshotWorkspace(t *testing.T, store bench.Store, key string) bench.Bench {
	t.Helper()
	rows, err := store.Snapshot(key)
	if err != nil || len(rows) != 1 {
		t.Fatalf("snapshot %s: %v", key, err)
	}
	return rows[0]
}

// A later checkpoint can advance one task ref while another repository rejects
// its push. Recovery must use the older complete seal without repairing source.
func TestRecoveryFacadeRestoresOlderSealAndPreservesPendingSource(t *testing.T) {
	store, spec, source := recoveryFixture(t)
	held, err := store.AcquireWorkspaceContext(context.Background(), source.Key, "source-owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range held.Workspace.Members {
		if err := os.WriteFile(filepath.Join(member.Path, "source.txt"), []byte("later "+member.Name), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, member.Path, "commit", "-am", "later source work")
	}
	rejected := held.Workspace.Members[1]
	hook := filepath.Join(rejected.Repository, ".git", "hooks", "update")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nif [ \"$1\" = \"refs/heads/"+rejected.Branch+"\" ]; then exit 1; fi\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	partial, err := store.CheckpointWorkspaceContext(context.Background(), source.Key, held.Lease.Token)
	if err == nil || partial == nil || partial.Workspace.Pending == nil {
		t.Fatalf("expected partial source checkpoint: %v", err)
	}
	if git(t, held.Workspace.Members[0].Repository, "rev-parse", "refs/heads/"+held.Workspace.Members[0].Branch) == source.Workspace.Members[0].Source {
		t.Fatal("fixture did not advance first remote")
	}
	beforeSource := snapshotWorkspace(t, store, source.Key)
	beforeFiles := tree(t, source.Path)
	staged, err := store.StageWorkspaceRecoveryContext(context.Background(), "recovery-create", spec)
	if err != nil {
		t.Fatal(err)
	}
	if staged.Workspace.ID == source.Workspace.ID || staged.Lease == nil || staged.Lease.Token == held.Lease.Token || staged.Lease.Holder != "initialization" || staged.Workspace.Phase != "initializing" {
		t.Fatal("recovery did not create a distinct initialization owner")
	}
	if staged.Workspace.Checkpoint != nil || staged.Workspace.Pending != nil || len(staged.Workspace.Preparation) != 0 {
		t.Fatal("recovery inherited source lifecycle proofs")
	}
	if staged.Workspace.RecoveredFrom == nil || !reflect.DeepEqual(*staged.Workspace.RecoveredFrom, spec.Source) {
		t.Fatal("exact recovery provenance missing")
	}
	for i, member := range staged.Workspace.Members {
		if member.Source != source.Workspace.Members[i].Source || git(t, member.Path, "rev-parse", "HEAD") != member.Source || member.Branch != "bench/recovered" {
			t.Fatal("recovery followed later task ref instead of sealed vector", member.Name)
		}
	}
	if !reflect.DeepEqual(beforeSource, snapshotWorkspace(t, store, source.Key)) || !reflect.DeepEqual(beforeFiles, tree(t, source.Path)) {
		t.Fatal("recovery changed source ownership, pending checkpoint, or files")
	}
	// Even another remote update cannot cause this creation identity to repin.
	for _, member := range staged.Workspace.Members {
		git(t, member.Repository, "commit", "--allow-empty", "-m", "remote moved again")
	}
	beforeReplay := tree(t, filepath.Dir(store.State))
	replay, err := store.StageWorkspaceRecoveryContext(context.Background(), "recovery-create", spec)
	if err != nil || !reflect.DeepEqual(staged, replay) {
		t.Fatal("recovery replay changed pinned identity", err)
	}
	if !reflect.DeepEqual(beforeReplay, tree(t, filepath.Dir(store.State))) {
		t.Fatal("recovery replay changed authoritative files")
	}
	ready, err := store.RecoverWorkspaceContext(context.Background(), "recovery-create", spec)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Lease != nil || ready.Workspace.Phase != "ready" || ready.Workspace.Checkpoint == nil || ready.Workspace.Checkpoint.Previous != "" || ready.Workspace.Checkpoint.GenerationID == source.Workspace.Checkpoint.GenerationID || ready.Workspace.Checkpoint.Seal == source.Workspace.Checkpoint.Seal {
		t.Fatal("recovery did not publish an independent genesis")
	}
	if !reflect.DeepEqual(beforeSource, snapshotWorkspace(t, store, source.Key)) || !reflect.DeepEqual(beforeFiles, tree(t, source.Path)) {
		t.Fatal("recovery completion changed pending source")
	}
}

func TestRecoveryFacadePublishesNewIdentityAndReplaysReadyResult(t *testing.T) {
	store, spec, source := recoveryFixture(t)
	ready, err := store.RecoverWorkspaceContext(context.Background(), "recovery-create", spec)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Workspace.ID == source.Workspace.ID || ready.Workspace.RecoveredFrom == nil || !reflect.DeepEqual(*ready.Workspace.RecoveredFrom, spec.Source) || ready.Lease != nil {
		t.Fatal("recovery lost provenance or kept initialization lease")
	}
	for i, member := range ready.Workspace.Members {
		if member.Source != source.Workspace.Members[i].Source || git(t, member.Repository, "rev-parse", "refs/heads/"+member.Branch) != member.Source {
			t.Fatal("new task branch does not publish sealed source")
		}
	}
	before := tree(t, filepath.Dir(store.State))
	replay, err := store.RecoverWorkspaceContext(context.Background(), "recovery-create", spec)
	if err != nil || !reflect.DeepEqual(ready, replay) || !reflect.DeepEqual(before, tree(t, filepath.Dir(store.State))) {
		t.Fatal("completed recovery did not replay exactly", err)
	}
}

func TestRecoveryFacadeRejectsInvalidEvidenceAndCollisionsBeforeCheckout(t *testing.T) {
	for _, name := range []string{"digest", "missing allowlist", "wrong allowlist", "source branch", "existing key", "existing path", "canceled"} {
		t.Run(name, func(t *testing.T) {
			store, spec, source := recoveryFixture(t)
			ctx := context.Background()
			switch name {
			case "digest":
				spec.Source.ManifestSHA256 = strings.Repeat("0", 64)
			case "missing allowlist":
				spec.Repositories = spec.Repositories[:1]
			case "wrong allowlist":
				spec.Repositories[1].Repository = spec.Repositories[0].Repository
			case "source branch":
				spec.Repositories[1].Branch = source.Workspace.Members[1].Branch
			case "existing key":
				spec.Key = source.Key
			case "existing path":
				path := filepath.Join(store.Data, "benches", spec.Key)
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "user-work"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := snapshotWorkspace(t, store, source.Key)
			beforeFiles := tree(t, source.Path)
			target := filepath.Join(store.Data, "benches", spec.Key)
			var targetFiles map[string]entry
			if name == "existing path" {
				targetFiles = tree(t, target)
			}
			result, err := store.StageWorkspaceRecoveryContext(ctx, "recovery-create", spec)
			if err == nil {
				t.Fatalf("invalid recovery accepted: %+v", result)
			}
			if name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
			if !reflect.DeepEqual(before, snapshotWorkspace(t, store, source.Key)) || !reflect.DeepEqual(beforeFiles, tree(t, source.Path)) {
				t.Fatal("rejected recovery changed source")
			}
			if name != "existing key" {
				if name == "existing path" {
					if !reflect.DeepEqual(targetFiles, tree(t, target)) {
						t.Fatal("collision changed user work")
					}
				} else if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Fatal("rejected recovery created checkout root", err)
				}
				if _, err := store.Snapshot(spec.Key); !errors.Is(err, bench.ErrNotFound) {
					t.Fatal("rejected recovery persisted target", err)
				}
			}
		})
	}
}

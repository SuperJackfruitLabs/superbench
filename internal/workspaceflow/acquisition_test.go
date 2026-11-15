package workspaceflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceAcquisitionRefreshesWithoutMovingLocalWorkAndReleasesWholeVector(t *testing.T) {
	store, spec, _ := fixture(t)
	ctx := context.Background()
	ready, err := store.InitializeWorkspaceContext(ctx, "acquire-create1", spec)
	if err != nil {
		t.Fatal(err)
	}
	genesis := ready.Workspace.Checkpoint.Seal
	for _, member := range ready.Workspace.Members {
		git(t, member.Repository, "commit", "--allow-empty", "-m", "new upstream")
	}
	held, err := store.AcquireWorkspaceContext(ctx, spec.Key, "stage-one")
	if err != nil {
		t.Fatal(err)
	}
	if held.Lease == nil || held.Lease.Holder != "stage-one" || held.Lease.Continuation == "" || len(held.Workspace.Preparation) != 2 || held.Workspace.Checkpoint.Seal != genesis {
		t.Fatal("acquisition did not prepare every member under one fresh owner")
	}
	for _, member := range held.Workspace.Members {
		if git(t, member.Path, "rev-parse", "HEAD") != member.Source {
			t.Fatal("acquisition moved the existing task checkout")
		}
		upstream := git(t, member.Repository, "rev-parse", "HEAD")
		if git(t, member.Mirror, "rev-parse", "refs/remotes/origin/main") != upstream {
			t.Fatal("acquisition did not refresh remote-tracking branches")
		}
	}
	if other, err := store.AcquireWorkspaceContext(ctx, spec.Key, "stage-one"); err == nil || other != nil {
		t.Fatal("same holder replaced or obtained the active ownership capability", err)
	}
	for _, member := range held.Workspace.Members {
		if err := os.WriteFile(filepath.Join(member.Path, "source.txt"), []byte("task change "+member.Name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ReleaseWorkspaceContext(ctx, spec.Key, held.Lease.Token); err == nil {
		t.Fatal("dirty aggregate released")
	}
	for _, member := range held.Workspace.Members {
		git(t, member.Path, "add", "source.txt")
		git(t, member.Path, "commit", "-m", "task change")
	}
	if _, err := store.ReleaseWorkspaceContext(ctx, spec.Key, held.Lease.Token); err == nil {
		t.Fatal("new committed vector released using the old checkpoint")
	}
	checkpointed, err := store.CheckpointWorkspaceContext(ctx, spec.Key, held.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if checkpointed.Workspace.Checkpoint.Seal == genesis || checkpointed.Workspace.Checkpoint.Previous != genesis {
		t.Fatal("stage checkpoint did not extend the genesis generation")
	}
	released, err := store.ReleaseWorkspaceContext(ctx, spec.Key, held.Lease.Token)
	if err != nil || released.Lease != nil || released.Workspace.Pending != nil {
		t.Fatal("complete checkpoint could not release", err)
	}
	next, err := store.AcquireWorkspaceContext(ctx, spec.Key, "stage-two")
	if err != nil || next.Lease == nil || next.Lease.Token == held.Lease.Token || next.Lease.Continuation == held.Lease.Continuation {
		t.Fatal("next acquisition did not establish fresh ownership", err)
	}
	snapshots, err := store.Snapshot(spec.Key)
	if err != nil || len(snapshots) != 1 || snapshots[0].Lease == nil || snapshots[0].Lease.Token != "" || snapshots[0].Lease.Continuation != "" {
		t.Fatal("status exposed aggregate ownership capabilities", err)
	}
	if _, err := store.ReleaseWorkspaceContext(ctx, spec.Key, held.Lease.Token); err == nil {
		t.Fatal("previous owner's token released a later stage")
	}
}

func TestWorkspaceRefreshFailureGrantsNoOwnerAndPreservesDirtyFiles(t *testing.T) {
	store, spec, root := fixture(t)
	ctx := context.Background()
	ready, err := store.InitializeWorkspaceContext(ctx, "refresh-create1", spec)
	if err != nil {
		t.Fatal(err)
	}
	member := ready.Workspace.Members[1]
	sentinel := filepath.Join(member.Path, "untracked-user-work")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	// Move only a disposable fixture remote. The registered mirror and checkout
	// remain in place; failed refresh must neither reclone nor clear local work.
	if err := os.Rename(member.Repository, filepath.Join(root, "unavailable-remote")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireWorkspaceContext(ctx, spec.Key, "stage"); err == nil {
		t.Fatal("unavailable second remote granted an ordinary acquisition")
	}
	status, err := store.Snapshot(spec.Key)
	if err != nil || len(status) != 1 || status[0].Lease != nil || status[0].Workspace.Checkpoint.Seal != ready.Workspace.Checkpoint.Seal {
		t.Fatal("failed refresh changed ownership or recovery point", err)
	}
	if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "preserve" {
		t.Fatal("failed refresh changed local work", err)
	}
}

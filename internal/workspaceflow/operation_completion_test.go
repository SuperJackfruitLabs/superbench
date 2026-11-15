package workspaceflow

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestActiveOperationBlocksSameTokenCompletionAndArchive(t *testing.T) {
	store, spec, _ := fixture(t)
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(store.Data, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink == 0 {
				return os.Chmod(path, 0700)
			}
			return nil
		})
	})
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 7, RequestID: "complete", Digest: strings.Repeat("a", 64)}, Key: spec.Key, Operation: "create", Epoch: strings.Repeat("b", 32)}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.StageWorkspaceContext(ctx, "operation-complete", spec)
	if err != nil {
		t.Fatal(err)
	}
	b, err = store.PrepareWorkspaceContext(ctx, b.Key, b.Lease.Token, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CheckpointWorkspaceContext(context.Background(), b.Key, b.Lease.Token); !errors.Is(err, operationcontrol.ErrBusy) {
		t.Fatal("same-token checkpoint crossed epoch", err)
	}
	if _, err = store.FinishWorkspaceInitializationContext(context.Background(), b.Key, b.Lease.Token); !errors.Is(err, operationcontrol.ErrBusy) {
		t.Fatal("same-token readiness crossed epoch", err)
	}
	ready, err := store.FinishWorkspaceInitializationContext(ctx, b.Key, b.Lease.Token)
	if err != nil {
		t.Fatal("owned completion refused", err)
	}
	if _, err = store.ArchiveWorkspaceContext(context.Background(), ready.Key, 0, nil); !errors.Is(err, operationcontrol.ErrBusy) {
		t.Fatal("archive crossed pre-receipt epoch", err)
	}
	rows, err := store.Snapshot(ready.Key)
	if err != nil || rows[0].Workspace.Archive != nil || rows[0].Lease != nil {
		t.Fatal(rows, err)
	}
}
func TestActiveOperationBlocksSameTokenRelease(t *testing.T) {
	store, _, ready := archiveFixture(t)
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 7, RequestID: "release", Digest: strings.Repeat("c", 64)}, Key: ready.Key, Operation: "lease", Epoch: strings.Repeat("d", 32)}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.AcquireWorkspaceOwnerContext(ctx, ready.Key, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReleaseWorkspaceContext(context.Background(), b.Key, b.Lease.Token); !errors.Is(err, operationcontrol.ErrBusy) {
		t.Fatal("release crossed active epoch", err)
	}
	if _, err = store.ReleaseWorkspaceContext(ctx, b.Key, b.Lease.Token); err != nil {
		t.Fatal("owned release refused", err)
	}
}
func TestActiveOperationBlocksInitializationRepairClaim(t *testing.T) {
	store, spec, _ := fixture(t)
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 7, RequestID: "repair", Digest: strings.Repeat("a", 64)}, Key: spec.Key, Operation: "create", Epoch: strings.Repeat("b", 32)}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.StageWorkspaceContext(ctx, "operation-repair", spec)
	if err != nil {
		t.Fatal(err)
	}
	b, err = store.PrepareWorkspaceContext(ctx, b.Key, b.Lease.Token, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.StopWorkspace(b.Key, b.Lease.Token, false); err != nil {
		t.Fatal(err)
	}
	store.State, err = filepath.EvalSymlinks(store.State)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := store.WorkspaceRepair()
	if err != nil {
		t.Fatal(err)
	}
	if plan, err := manager.Inspect(context.Background(), b.Key, nil); !errors.Is(err, operationcontrol.ErrBusy) || plan != nil {
		t.Fatal("repair inspection crossed active epoch", plan, err)
	}
	rows, err := store.Snapshot(b.Key)
	if err != nil || !rows[0].Lease.StopRequested {
		t.Fatal(rows, err)
	}

	if err = store.CheckWorkspaceOwner(b.Key, b.Lease.Token, "initialization"); err != nil {
		t.Fatal("repair changed stopped owner", err)
	}
}

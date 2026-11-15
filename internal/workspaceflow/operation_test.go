package workspaceflow

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func TestStagingPersistsExactOperationEpochBeforeOwnerHandoff(t *testing.T) {
	store, spec, _ := fixture(t)
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 42, RequestID: "stage", Digest: strings.Repeat("a", 64)}, Key: spec.Key, Operation: "create", Epoch: strings.Repeat("b", 32)}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.StageWorkspaceContext(ctx, "create-operation", spec)
	if err != nil {
		t.Fatal(err)
	}
	db, err := statestore.Open(filepath.Join(store.State, "bench.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	binding, err := operationcontrol.Get(db, i.Target)
	if err != nil || binding.Phase != "bound" || binding.BenchID != b.Workspace.ID {
		t.Fatal("owner escaped without operation binding", binding, err)
	}
	if _, err = store.StageWorkspaceContext(context.Background(), "create-operation", spec); !errors.Is(err, operationcontrol.ErrBusy) {
		t.Fatal("unbound caller inherited operation", err)
	}
	if _, err = store.PrepareWorkspaceContext(context.Background(), b.Key, b.Lease.Token, "", false); !errors.Is(err, operationcontrol.ErrBusy) {
		t.Fatal("same-token local caller crossed active operation", err)
	}
	if _, err = store.PrepareWorkspaceContext(ctx, b.Key, b.Lease.Token, "", false); err != nil {
		t.Fatal("exact operation cannot prepare", err)
	}
}

package workspaceflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func TestScopedStagingRefusesAbsentOrHistoricalStateWithoutEffects(t *testing.T) {
	for _, kind := range []string{"absent", "historical"} {
		t.Run(kind, func(t *testing.T) {
			store, spec, root := fixture(t)
			if kind == "historical" {
				if err := store.Migrate(); err != nil {
					t.Fatal(err)
				}
				db, err := statestore.Open(filepath.Join(store.State, "bench.db"), false)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("PRAGMA user_version=25")
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			i := operationcontrol.Intent{Target: operationcontrol.Target{RequestID: "stage", Digest: strings.Repeat("a", 64)}, Key: spec.Key, Operation: "create", Epoch: strings.Repeat("b", 32)}
			ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
			if err != nil {
				t.Fatal(err)
			}
			before := tree(t, root)
			if b, err := store.StageWorkspaceContext(ctx, "creation", spec); err == nil || b != nil {
				t.Fatal("scoped request migrated/admitted state", b, err)
			}
			if after := tree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("refused operation changed fixture")
			}
			if _, err := os.Stat(store.Data); !os.IsNotExist(err) {
				t.Fatal("data created", err)
			}
		})
	}
}

func TestScopedStagingPreservesOtherOperationGitControl(t *testing.T) {
	store, spec, _ := fixture(t)
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(store.State, "git-controls")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, strings.Repeat("c", 32)+".json")
	raw := []byte(`{"Version":"superbench.git-control.v2","Root":"` + store.CgroupRoot + `","ID":"` + strings.Repeat("c", 32) + `","HostID":"` + strings.Repeat("d", 64) + `","BootID":"fixture","OperationID":"` + strings.Repeat("e", 64) + `","OperationEpoch":"` + strings.Repeat("f", 32) + `"}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StageWorkspaceContext(context.Background(), "creation", spec); !errors.Is(err, operationcontrol.ErrBusy) {
		t.Fatal("ordinary global recovery crossed attributed control", err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{RequestID: "stage", Digest: strings.Repeat("a", 64)}, Key: spec.Key, Operation: "create", Epoch: strings.Repeat("b", 32)}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.StageWorkspaceContext(ctx, "creation", spec); err != nil {
		t.Fatal("scoped stage globally recovered another operation", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("unrelated control changed", err)
	}
}

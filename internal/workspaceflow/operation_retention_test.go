package workspaceflow

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func TestReleasedRunCannotBeCollectedBeforeOperationCutoff(t *testing.T) {
	store, _, ready := archiveFixture(t)
	state, err := filepath.EvalSymlinks(store.State)
	if err != nil {
		t.Fatal(err)
	}
	store.State = state
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 42, RequestID: "released-window", Digest: strings.Repeat("a", 64)}, Key: ready.Key, Operation: "run", Epoch: strings.Repeat("b", 32)}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	b := *ready
	id := strings.Repeat("c", 32)
	b.Lease = &model.Lease{Holder: "owner", Token: strings.Repeat("d", 32), Continuation: strings.Repeat("e", 32), Execution: &model.Execution{ID: id, Root: "/owned-scope", HostID: strings.Repeat("f", 64), BootID: "boot", Phase: "starting", Log: filepath.Join(state, "run-"+id+".log"), LogLimitBytes: 1024}}
	log := []byte("sealed output retained across receipt cutoff")
	if err = os.WriteFile(b.Lease.Execution.Log, log, 0600); err != nil {
		t.Fatal(err)
	}
	entry, err := retention.Snapshot(state, id)
	if err != nil {
		t.Fatal(err)
	}
	db, err := statestore.Open(filepath.Join(state, "bench.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	commit := func(fn func(*sql.Tx) error) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = fn(tx); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	commit(func(tx *sql.Tx) error {
		if err := operationcontrol.Begin(tx, i, b.Workspace.ID); err != nil {
			return err
		}
		if err := operationcontrol.Bind(tx, i, &b); err != nil {
			return err
		}
		if err := operationcontrol.StartHistory(ctx, tx, &b, history.Record{ID: id, Key: b.Key, Holder: b.Lease.Holder, StartedAt: time.Now().UTC()}); err != nil {
			return err
		}
		exit := 0
		if err := history.Seal(tx, id, entry, int64(len(log)), &exit, false); err != nil {
			return err
		}
		return history.Released(tx, id, ready.Workspace.Checkpoint.Seal)
	})
	// The workspace row is unleased, exactly the legitimate ReleaseHistory→receipt
	// commit window. The operation's launch provenance is still active.
	plan, err := store.PlanCollection(0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range plan.Logs {
		if item.Record.ID == id {
			t.Fatal("active operation's released output became deletable")
		}
	}
	commit(func(tx *sql.Tx) error { return operationcontrol.Finish(tx, i) })
	plan, err = store.PlanCollection(0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range plan.Logs {
		found = found || item.Record.ID == id
	}
	if !found {
		t.Fatal("completed operation permanently pinned released output")
	}
}

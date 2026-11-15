package workspacetransfer_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func TestContinuationInitialAuthorizedViewCancellationRevokesOnlyOriginalOwner(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "present"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			for _, mode := range []string{"authorized", "wrong-source", "malformed-plan", "already-cancelled"} {
				t.Run(mode, func(t *testing.T) {
					var f *fixture
					var id string
					if missing {
						f, p := missingFixture(t)
						id = p.ID
						_, err := f.m.BeginMissing(context.Background(), *p)
						must(t, err)
						runCancellationCase(t, f, id, missing, mode)
					} else {
						f = newFixture(t)
						p := f.plan()
						id = p.ID
						_, err := f.m.Begin(context.Background(), *p)
						must(t, err)
						runCancellationCase(t, f, id, missing, mode)
					}
				})
			}
		})
	}
}

func runCancellationCase(t *testing.T, f *fixture, id string, missing bool, mode string) {
	t.Helper()
	must(t, f.sql(func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER hold_target BEFORE UPDATE ON benches WHEN NEW.key='target' AND json_extract(NEW.record,'$.Workspace.Phase')='ready' BEGIN SELECT RAISE(ABORT,'hold original initialization'); END`)
		return err
	}))
	var err error
	if missing {
		_, err = f.m.ContinueMissing(context.Background(), "source", id)
	} else {
		_, err = f.m.Continue(context.Background(), "source", id)
	}
	if err == nil {
		t.Fatal("fixture unexpectedly completed")
	}
	before := f.row("target")
	if before == nil || before.Lease == nil || before.Lease.StopRequested || before.Lease.Continuation == "" {
		t.Fatal("fixture did not retain runnable original initialization")
	}
	if mode == "malformed-plan" {
		must(t, f.sql(func(tx *sql.Tx) error {
			_, err := tx.Exec("UPDATE workspace_ref_transfers SET plan=? WHERE id=?", []byte(`{`), id)
			return err
		}))
	}
	state := f.stateBytes()
	a, err := f.store.WorkspaceRepair()
	must(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := "source"
	if mode == "wrong-source" {
		source = "different"
	}
	if mode == "already-cancelled" {
		cancel()
		if missing {
			_, err = f.m.ContinueMissing(ctx, source, id)
		} else {
			_, err = f.m.Continue(ctx, source, id)
		}
	} else {
		update := a.Prep.Update
		first := true
		a.Prep.Update = func(fn func(benchstate.Database, *sql.Tx) error) error {
			if !first {
				return update(fn)
			}
			first = false
			return update(func(rows benchstate.Database, tx *sql.Tx) error {
				callbackErr := fn(rows, tx)
				// Cancellation after the authorized view, before its SQL transaction
				// returns, must still reconcile its exact owner. Unauthorized reads never do.
				cancel()
				return errors.Join(callbackErr, ctx.Err())
			})
		}
		out, e := f.m.ContinueWithAdminForTest(ctx, source, id, a, missing)
		err = e
		if out != nil && out.GrantToken != "" {
			t.Fatal("cancelled continuation returned grant")
		}
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("missing cancellation", err)
	}
	after := f.row("target")
	if mode != "authorized" {
		if !bytes.Equal(state, f.stateBytes()) {
			t.Fatal("unauthorized/pre-cancelled continuation changed authority")
		}
		return
	}
	if after.Lease == nil || after.Lease.Token != before.Lease.Token || !after.Lease.StopRequested || after.Lease.Continuation != "" {
		t.Fatal("authorized cancellation did not revoke exact original owner")
	}
	expected := workspace.Clone(before)
	expected.Lease.StopRequested = true
	expected.Lease.Continuation = ""
	if !reflect.DeepEqual(expected, after) {
		t.Fatal("cancellation changed evidence beyond stop authority")
	}
}

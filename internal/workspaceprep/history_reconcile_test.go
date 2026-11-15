package workspaceprep_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

// The payload really executes, but injected persistence failure leaves its
// supervised intent unsealed. Explicit admin stop supplies revoked ownership.
func interruptedWorkspace(t *testing.T) (*fixture, *model.Bench) {
	t.Helper()
	f := newFixture(t, 1, nil)
	physical, err := filepath.EvalSymlinks(f.manager.State)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.State = physical
	f.ready()
	if _, err := f.prepare(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	seal := f.manager.Seal
	f.manager.Seal = func(string, *output.Capture, *int, bool) error { return errors.New("interrupted history persistence") }
	run, err := f.run("", "/bin/sh", "-c", "printf retained-interrupted-output")
	f.manager.Seal = seal
	if err == nil || run == nil || run.Execution == nil {
		t.Fatal("fixture did not preserve an unsealed payload", err)
	}
	stopped, err := f.manager.Stop("card", "", true)
	if err != nil || stopped.Lease.Continuation != "" || !stopped.Lease.StopRequested {
		t.Fatal("fixture stop did not revoke launch", err)
	}
	return f, stopped
}

func historyRow(t *testing.T, f *fixture, id string) history.Record {
	t.Helper()
	db, err := statestore.Open(filepath.Join(f.store.State, "bench.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, err := history.Get(db, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func setupRows(t *testing.T, f *fixture) []string {
	t.Helper()
	db, err := statestore.Open(filepath.Join(f.store.State, "bench.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT bench,digest,run_id,completed FROM setup_receipts ORDER BY bench")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a, b, c, d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		out = append(out, a+"|"+b+"|"+c+"|"+d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInterruptedHistoryPresentAbsentAtomicRepairAndReleasedReplay(t *testing.T) {
	for _, absent := range []bool{false, true} {
		name := "present"
		if absent {
			name = "absent"
		}
		t.Run(name, func(t *testing.T) {
			f, stopped := interruptedWorkspace(t)
			id, path := stopped.Lease.Execution.ID, stopped.Lease.Execution.Log
			if absent {
				// Model a missing interrupted artifact in this owned test fixture;
				// the repair itself must never recreate it or infer empty output.
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			before, receipts := historyRow(t, f, id), setupRows(t, f)
			plan, err := f.manager.InspectHistory("card")
			if err != nil || plan == nil || (plan.File == nil) != absent {
				t.Fatal("inspect interrupted payload", err)
			}
			raw, err := plan.Bytes()
			if err != nil || strings.Contains(string(raw), stopped.Lease.Token) || strings.Contains(string(raw), "retained-interrupted-output") {
				t.Fatal("plan leaked capability or command", err)
			}
			parsed, err := workspaceprep.ParseHistoryPlan(raw)
			if err != nil || !reflect.DeepEqual(parsed, *plan) {
				t.Fatal("canonical plan did not roundtrip", err)
			}
			observed, err := f.manager.ReconcileHistory(parsed)
			if err != nil || observed.Interruption == nil || observed.Interruption.LogState != name || observed.ExitCode != nil || observed.Truncated || observed.Origin != before.Origin || observed.Holder != before.Holder || !observed.StartedAt.Equal(before.StartedAt) || !observed.ReleasedAt.IsZero() {
				t.Fatal("repair invented an outcome or lost provenance", observed, err)
			}
			if !reflect.DeepEqual(f.read(), stopped) || !reflect.DeepEqual(receipts, setupRows(t, f)) {
				t.Fatal("repair changed lease, preparation or once receipts")
			}
			creates, starts := f.backend.counts()
			if _, err := f.run("", "/bin/sh", "-c", "echo forbidden"); err == nil {
				t.Fatal("observed interrupted history revived continuation")
			}
			if c, s := f.backend.counts(); c != creates || s != starts {
				t.Fatal("repair permitted a new scope")
			}
			// Unknown history cannot become the success proof of an actual hook.
			err = f.update(func(rows benchstate.Database, tx *sql.Tx) error {
				fake := workspace.Clone(rows["card"])
				fake.Workspace.Preparation[0].RunID = id
				return workspaceprep.ReleaseHistory(tx, fake)
			})
			if err == nil || !reflect.DeepEqual(f.read(), stopped) {
				t.Fatal("unknown observation accepted as preparation proof")
			}
			// Caller explicitly commits its own setup outputs; repair never does.
			for _, m := range stopped.Workspace.Members {
				git(t, m.Path, "add", ".")
				git(t, m.Path, "commit", "-m", "preserve owned preparation output")
			}
			if _, err := f.lifecycle().Checkpoint(context.Background(), "card", stopped.Lease.Token); err != nil {
				t.Fatal(err)
			}
			beforeRelease := f.read()
			priorRuns := map[string]history.Record{}
			for _, retained := range beforeRelease.Lease.RetainedRuns {
				priorRuns[retained] = historyRow(t, f, retained)
			}
			if err := f.update(func(_ benchstate.Database, tx *sql.Tx) error {
				_, err := tx.Exec("CREATE TRIGGER fail_interrupted_release BEFORE UPDATE ON run_history WHEN OLD.id='" + id + "' BEGIN SELECT RAISE(FAIL,'injected final history release failure'); END")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.lifecycle().Release(context.Background(), "card", stopped.Lease.Token); err == nil {
				t.Fatal("release ignored final history persistence failure")
			}
			if !reflect.DeepEqual(f.read(), beforeRelease) || !reflect.DeepEqual(receipts, setupRows(t, f)) || !reflect.DeepEqual(historyRow(t, f, id), observed) {
				t.Fatal("failed release removed ownership or changed interrupted evidence")
			}
			for retained, prior := range priorRuns {
				if !reflect.DeepEqual(historyRow(t, f, retained), prior) {
					t.Fatal("failed final release leaked an earlier run release")
				}
			}
			if err := f.update(func(_ benchstate.Database, tx *sql.Tx) error {
				_, err := tx.Exec("DROP TRIGGER fail_interrupted_release")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.lifecycle().Release(context.Background(), "card", stopped.Lease.Token); err != nil {
				t.Fatal("observed unknown outcome could not release after explicit checkpoint", err)
			}
			released := historyRow(t, f, id)
			if released.ReleasedAt.IsZero() || released.Checkpoint == "" || released.ExitCode != nil || f.read().Lease != nil {
				t.Fatal("release invented outcome or kept owner")
			}
			replay, err := f.manager.ReconcileHistory(parsed)
			if err != nil || !reflect.DeepEqual(replay, released) || !reflect.DeepEqual(receipts, setupRows(t, f)) {
				t.Fatal("exact historical approval replay changed released evidence", err)
			}
			if absent {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("repair created an empty replacement", err)
				}
			}
		})
	}
}

type historyBackend struct {
	*backend
	fenceErr, emptyErr error
	empties            int
	failFinal          bool
}

func (b *historyBackend) Fence(e *execution.Execution) error {
	if b.fenceErr != nil {
		return b.fenceErr
	}
	return b.backend.Fence(e)
}
func (b *historyBackend) Empty(e *execution.Execution) error {
	b.empties++
	if b.emptyErr != nil || b.failFinal && b.empties == 2 {
		return errors.New("scope not independently empty")
	}
	return b.backend.Empty(e)
}

func TestInterruptedHistoryLiveCaptureAndFenceFailurePreserveAllState(t *testing.T) {
	for _, failure := range []string{"capture", "fence", "empty", "final-empty"} {
		t.Run(failure, func(t *testing.T) {
			f, b := interruptedWorkspace(t)
			before, receipts := historyRow(t, f, b.Lease.Execution.ID), setupRows(t, f)
			plan, err := f.manager.InspectHistory("card")
			if err != nil {
				t.Fatal(err)
			}
			backend := &historyBackend{backend: f.backend}
			f.manager.Backend = backend
			switch failure {
			case "capture":
				writer, err := os.OpenFile(b.Lease.Execution.Log, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
				if err := syscall.Flock(int(writer.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			case "fence":
				backend.fenceErr = errors.New("changed boot identity")
			case "empty":
				backend.emptyErr = errors.New("scope remains populated")
			case "final-empty":
				backend.failFinal = true
			}
			if _, err := f.manager.ReconcileHistory(*plan); err == nil {
				t.Fatal("unsafe scope/capture observation accepted")
			}
			if !reflect.DeepEqual(f.read(), b) || !reflect.DeepEqual(historyRow(t, f, before.ID), before) || !reflect.DeepEqual(setupRows(t, f), receipts) {
				t.Fatal("failed reconciliation changed authority or history")
			}
		})
	}
}

func TestInterruptedHistoryChangedEvidenceOrPlanCannotApply(t *testing.T) {
	for _, change := range []string{"bytes", "replace", "oversize", "absent-appeared", "bench", "history", "scope", "pid", "future", "digest", "unknown-field", "duplicate-field"} {
		t.Run(change, func(t *testing.T) {
			f, b := interruptedWorkspace(t)
			path := b.Lease.Execution.Log
			if change == "absent-appeared" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := f.manager.InspectHistory("card")
			if err != nil {
				t.Fatal(err)
			}
			if change == "unknown-field" || change == "duplicate-field" {
				raw, err := plan.Bytes()
				if err != nil {
					t.Fatal(err)
				}
				field := `"unexpected":true,`
				if change == "duplicate-field" {
					field = `"Key":"card",`
				}
				if _, err := workspaceprep.ParseHistoryPlan(append([]byte("{"+field), raw[1:]...)); err == nil {
					t.Fatal("ambiguous plan accepted")
				}
				return
			}
			switch change {
			case "bytes", "absent-appeared":
				err = os.WriteFile(path, []byte("changed log bytes"), 0600)
			case "replace":
				err = os.Rename(path, path+".retained")
				if err == nil {
					err = os.WriteFile(path, []byte("retained-interrupted-output"), 0600)
				}
			case "oversize":
				err = os.WriteFile(path, make([]byte, b.Lease.Execution.LogLimitBytes+1), 0600)
			case "bench":
				err = f.update(func(rows benchstate.Database, _ *sql.Tx) error {
					rows["card"].Workspace.Preparation = nil
					return nil
				})
			case "history":
				err = f.update(func(_ benchstate.Database, tx *sql.Tx) error {
					r, err := history.Get(tx, b.Lease.Execution.ID)
					if err != nil {
						return err
					}
					r.Holder = "changed-after-review"
					raw, err := json.Marshal(r)
					if err != nil {
						return err
					}
					_, err = tx.Exec("UPDATE run_history SET record=? WHERE id=?", string(raw), r.ID)
					return err
				})
			case "scope":
				plan.Execution.BootID = "different-boot"
				resignHistoryPlan(t, plan)
			case "pid":
				plan.Execution.PID++
				resignHistoryPlan(t, plan)
			case "future":
				plan.ObservedAt = time.Now().UTC().Add(time.Hour)
				resignHistoryPlan(t, plan)
			case "digest":
				plan.Digest = strings.Repeat("0", 64)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, row := f.read(), historyRow(t, f, b.Lease.Execution.ID)
			if _, err := f.manager.ReconcileHistory(*plan); err == nil {
				t.Fatal("stale evidence or forged plan accepted")
			}
			if !reflect.DeepEqual(f.read(), before) || !reflect.DeepEqual(historyRow(t, f, row.ID), row) {
				t.Fatal("stale approval mutated state")
			}
		})
	}
}

func TestInterruptedHistoryCommitRollbackRetainsLockAndAllowsExactRetry(t *testing.T) {
	f, b := interruptedWorkspace(t)
	plan, err := f.manager.InspectHistory("card")
	if err != nil {
		t.Fatal(err)
	}
	before, receipts := historyRow(t, f, b.Lease.Execution.ID), setupRows(t, f)
	update := f.manager.Update
	checked := false
	f.manager.Update = func(change func(benchstate.Database, *sql.Tx) error) error {
		return update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := change(rows, tx); err != nil {
				return err
			}
			r, err := history.Get(tx, before.ID)
			if err != nil || r.Interruption == nil {
				t.Fatal("observation not saved inside transaction", err)
			}
			writer, err := os.OpenFile(b.Lease.Execution.Log, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if err := syscall.Flock(int(writer.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
				t.Fatal("capture exclusion released before transaction completed")
			}
			checked = true
			return errors.New("injected SQL commit failure")
		})
	}
	if _, err := f.manager.ReconcileHistory(*plan); err == nil || !checked {
		t.Fatal("commit failure not observed")
	}
	if !reflect.DeepEqual(f.read(), b) || !reflect.DeepEqual(historyRow(t, f, before.ID), before) || !reflect.DeepEqual(setupRows(t, f), receipts) {
		t.Fatal("rollback leaked observation, ownership or once receipt change")
	}
	f.manager.Update = update
	if _, err := f.manager.ReconcileHistory(*plan); err != nil {
		t.Fatal("exact retry after rollback failed", err)
	}
}

func resignHistoryPlan(t *testing.T, p *workspaceprep.HistoryPlan) {
	t.Helper()
	p.Digest = ""
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	p.Digest = hex.EncodeToString(sum[:])
}

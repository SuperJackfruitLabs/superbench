package operationcancel

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
)

func TestConcludedCancellationSurvivesReviewedLogAndHistoryCollection(t *testing.T) {
	m, db, i, b, _ := fixture(t)
	id, log := b.Lease.Execution.ID, b.Lease.Execution.Log
	if err := os.WriteFile(log, []byte("retained actual output"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.ReconcileInterruption(i); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(func(_ benchstate.Database, tx *sql.Tx) error {
		return history.Released(tx, id, strings.Repeat("9", 40))
	}); err != nil {
		t.Fatal(err)
	}
	b.Lease = nil
	b.Workspace.Phase = "ready"
	if err := m.Conclude(i); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		protected, err := operationcontrol.ProtectedRuns(db)
		if err != nil || protected[id] {
			t.Fatal("terminal operation still pins history", protected, err)
		}
		p, err := retention.Build(db, m.State, protected, 0, 0, retention.MaxItems)
		if err != nil {
			t.Fatal(err)
		}
		if pass == 0 && len(p.Logs) != 1 || pass == 1 && len(p.Records) != 1 {
			t.Fatalf("wrong real retention plan: %+v", p)
		}
		if err = m.Update(func(_ benchstate.Database, tx *sql.Tx) error { return retention.Prepare(tx, m.State, *p, protected) }); err != nil {
			t.Fatal(err)
		}
		if err = m.Update(func(_ benchstate.Database, tx *sql.Tx) error {
			_, err := retention.Apply(tx, m.State, *p, protected, nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := history.Get(db, id); err != sql.ErrNoRows {
		t.Fatal("history not actually pruned", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("log not actually collected", err)
	}
	m.CheckBarrier = func() error { return nil }
	m.RecoverGit = func(operationcontrol.Control, operationcontrol.Intent) error {
		t.Fatal("terminal replay attempted Git recovery")
		return nil
	}
	if err := m.Reconcile(context.Background(), i, nil); err != nil {
		t.Fatal("exact concluded cancellation could not finalize receipt after retention", err)
	}
}

func TestTerminalReplayRequiresExactCancellationConclusion(t *testing.T) {
	for _, kind := range []string{"normal-finish", "missing-marker", "foreign-epoch", "foreign-digest", "foreign-uid", "changed-control", "missing-operation", "unconcluded"} {
		t.Run(kind, func(t *testing.T) {
			m, db, i, _, _ := fixture(t)
			if err := m.ReconcileInterruption(i); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "normal-finish":
				if err := m.Update(func(_ benchstate.Database, tx *sql.Tx) error { return operationcontrol.Finish(tx, i) }); err != nil {
					t.Fatal(err)
				}
			case "unconcluded":
			default:
				if err := m.Conclude(i); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "missing-marker":
					if _, err := db.Exec(`DELETE FROM workspace_operation_cancellations`); err != nil {
						t.Fatal(err)
					}
				case "foreign-epoch":
					i.Epoch = strings.Repeat("9", 32)
				case "foreign-digest":
					i.Target.Digest = strings.Repeat("9", 64)
				case "foreign-uid":
					i.Target.UID++
				case "changed-control":
					if _, err := db.Exec(`UPDATE workspace_operations SET control_boot='changed'`); err != nil {
						t.Fatal(err)
					}
				case "missing-operation":
					if _, err := db.Exec(`DELETE FROM workspace_operation_cancellations; DELETE FROM workspace_operation_runs; DELETE FROM workspace_operations`); err != nil {
						t.Fatal(err)
					}
				}
			}
			m.CheckBarrier = func() error { return nil }
			m.RecoverGit = func(operationcontrol.Control, operationcontrol.Intent) error { return operationcontrol.ErrAuthority }
			if err := m.Reconcile(context.Background(), i, nil); err == nil {
				t.Fatal("invented cancellation conclusion", kind)
			}
		})
	}
}

func TestIgnoredConclusionSaveRollsBackTerminalPhase(t *testing.T) {
	m, db, i, _, _ := fixture(t)
	if err := m.ReconcileInterruption(i); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER ignore_conclusion BEFORE INSERT ON workspace_operation_cancellations BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if err := m.Conclude(i); err == nil {
		t.Fatal("ignored conclusion acknowledged")
	}
	b, err := operationcontrol.Get(db, i.Target)
	if err != nil || b.Phase != "bound" {
		t.Fatal("terminal phase escaped failed conclusion save", b, err)
	}
	if _, err = db.Exec(`DROP TRIGGER ignore_conclusion`); err != nil {
		t.Fatal(err)
	}
	if err = m.Conclude(i); err != nil {
		t.Fatal(err)
	}
}

func TestConcludedCancellationDoesNotFollowLaterOwnerOrChangedMarker(t *testing.T) {
	m, db, i, b, f := fixture(t)
	if err := m.ReconcileInterruption(i); err != nil {
		t.Fatal(err)
	}
	if err := m.Conclude(i); err != nil {
		t.Fatal(err)
	}
	b.Lease.Token = strings.Repeat("9", 32)
	b.Lease.StopRequested = false
	b.Lease.Continuation = strings.Repeat("8", 32)
	before := f.count
	m.CheckBarrier = func() error { return nil }
	m.RecoverGit = func(operationcontrol.Control, operationcontrol.Intent) error {
		t.Fatal("old terminal replay tried Git recovery")
		return nil
	}
	if err := m.Reconcile(context.Background(), i, nil); err != nil {
		t.Fatal(err)
	}
	if f.count != before || b.Lease.StopRequested || b.Lease.Token != strings.Repeat("9", 32) {
		t.Fatal("terminal retry followed later owner")
	}
	if _, err := db.Exec(`UPDATE workspace_operation_cancellations SET record=record||' '`); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background(), i, nil); err == nil {
		t.Fatal("noncanonical conclusion adopted")
	}
	if f.count != before || b.Lease.StopRequested {
		t.Fatal("invalid marker affected later owner")
	}
}

func TestNormalFinishedEpochRequiresFirstFullCancellationProof(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "intact", true: "collected-before-first-conclusion"}[missing], func(t *testing.T) {
			m, db, i, b, f := fixture(t)
			id, log := b.Lease.Execution.ID, b.Lease.Execution.Log
			if err := os.WriteFile(log, []byte("actual sealed output"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := m.ReconcileInterruption(i); err != nil {
				t.Fatal(err)
			}
			if err := m.Update(func(_ benchstate.Database, tx *sql.Tx) error { return operationcontrol.Finish(tx, i) }); err != nil {
				t.Fatal(err)
			}
			if ok, err := operationcontrol.CancellationConcluded(db, i); err != nil || ok {
				t.Fatal("ordinary finish invented cancellation", ok, err)
			}
			// A later owner must remain independent while the old epoch proves only
			// its own already-sealed runs, helper denies and old Git controls.
			b.Lease.Token = strings.Repeat("9", 32)
			b.Lease.StopRequested = false
			b.Lease.Continuation = strings.Repeat("8", 32)
			if missing {
				if _, err := db.Exec(`DELETE FROM run_history WHERE id=?`, id); err != nil {
					t.Fatal(err)
				}
			}
			recovered := 0
			m.CheckBarrier = func() error { return nil }
			m.RecoverGit = func(operationcontrol.Control, operationcontrol.Intent) error { recovered++; return nil }
			before := f.count
			err := m.Reconcile(context.Background(), i, nil)
			if missing && err == nil || !missing && err != nil {
				t.Fatal("wrong first-proof result", err)
			}
			if recovered != 1 || f.count != before || b.Lease.StopRequested || b.Lease.Token != strings.Repeat("9", 32) {
				t.Fatal("first proof skipped controls or followed later owner", recovered, b)
			}
			ok, e := operationcontrol.CancellationConcluded(db, i)
			if e != nil || ok == missing {
				t.Fatal("wrong durable outcome", ok, e)
			}
		})
	}
}

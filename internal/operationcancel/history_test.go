package operationcancel

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

type fence struct{ count int }

func (f *fence) Fence(*model.Execution) error { f.count++; return nil }
func (f *fence) Empty(*model.Execution) error { return nil }
func fixture(t *testing.T) (Manager, *sql.DB, operationcontrol.Intent, *model.Bench, *fence) {
	t.Helper()
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := statestore.Open(filepath.Join(state, "test.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 42, RequestID: "managed", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "setup", Epoch: strings.Repeat("b", 32)}
	c := operationcontrol.Control{State: state, Root: "/managed-scope", HostID: strings.Repeat("c", 64), BootID: "boot"}
	b := &model.Bench{Key: i.Key, Workspace: &model.Workspace{ID: strings.Repeat("d", 32)}, Lease: &model.Lease{Holder: "owner", Token: strings.Repeat("e", 32), Continuation: strings.Repeat("f", 32)}}
	f := &fence{}
	m := Manager{State: state, Backend: f, Gate: func(_ string, fn func() error) error { return fn() }, Update: func(fn func(benchstate.Database, *sql.Tx) error) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = fn(benchstate.Database{i.Key: b}, tx); err != nil {
			return err
		}
		return tx.Commit()
	}}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = operationcontrol.WithControl(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	err = m.Update(func(_ benchstate.Database, tx *sql.Tx) error {
		if err := operationcontrol.Schema(tx); err != nil {
			return err
		}
		if err := history.Schema(tx); err != nil {
			return err
		}
		if err := operationcontrol.BeginWithControl(tx, i, b.Workspace.ID, c); err != nil {
			return err
		}
		if err := operationcontrol.Bind(tx, i, b); err != nil {
			return err
		}
		id := strings.Repeat("1", 32)
		b.Lease.Execution = &model.Execution{ID: id, Root: c.Root, HostID: c.HostID, BootID: c.BootID, Phase: "starting", Log: filepath.Join(state, "run-"+id+".log"), LogLimitBytes: 4096}
		return operationcontrol.StartHistory(ctx, tx, b, history.Record{ID: id, Key: b.Key, Holder: b.Lease.Holder, StartedAt: time.Now().UTC()})
	})
	if err != nil {
		t.Fatal(err)
	}
	b.Lease.StopRequested = true
	b.Lease.Continuation = ""
	b.Lease.Execution.Phase = "stopped"
	return m, db, i, b, f
}

func TestManagedInterruptionPreservesPartialBytesOrActualAbsence(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "partial"}[present], func(t *testing.T) {
			m, db, i, b, f := fixture(t)
			e := b.Lease.Execution
			raw := []byte("incomplete output\x00still evidence")
			if present {
				if err := os.WriteFile(e.Log, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.ReconcileInterruption(i); err != nil {
				t.Fatal(err)
			}
			r, err := history.Get(db, e.ID)
			if err != nil || r.Interruption == nil || r.ExitCode != nil || r.Truncated || r.Origin != "supervised" || r.SealedAt.IsZero() || !r.ReleasedAt.IsZero() || f.count != 1 {
				t.Fatal(r, f.count, err)
			}
			after, err := os.ReadFile(e.Log)
			if present {
				if err != nil || string(after) != string(raw) || r.File == nil {
					t.Fatal("partial bytes lost", r, err)
				}
			} else if !os.IsNotExist(err) || r.File != nil {
				t.Fatal("absence became capture", r, err)
			}
			if len(b.Workspace.Preparation) != 0 || b.Workspace.Checkpoint != nil || !b.Lease.StopRequested || b.Lease.Continuation != "" {
				t.Fatal("interruption became completion", b)
			}
			if err = m.ReconcileInterruption(i); err != nil {
				t.Fatal("exact retry failed", err)
			}
		})
	}
}

func TestManagedInterruptionRefusesActiveCaptureAndSQLFailurePreservesIntent(t *testing.T) {
	m, db, i, b, _ := fixture(t)
	e := b.Lease.Execution
	if err := os.WriteFile(e.Log, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(e.Log, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err = m.ReconcileInterruption(i); err == nil {
		t.Fatal("live capturer adopted")
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TRIGGER refuse_observation BEFORE UPDATE ON run_history BEGIN SELECT RAISE(FAIL,'fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err = m.ReconcileInterruption(i); err == nil {
		t.Fatal("failed SQL acknowledged")
	}
	r, err := history.Get(db, e.ID)
	if err != nil || r.Interruption != nil || !r.SealedAt.IsZero() {
		t.Fatal("rollback lost original intent", r, err)
	}
	if _, err = db.Exec(`DROP TRIGGER refuse_observation`); err != nil {
		t.Fatal(err)
	}
	if err = m.ReconcileInterruption(i); err != nil {
		t.Fatal("exact retry failed", err)
	}
}

func TestManagedInterruptionCannotAdoptOlderRunOrLaterOwner(t *testing.T) {
	for _, change := range []string{"unlabelled", "epoch", "owner", "scope", "unrevoked", "cooperative", "changed_history"} {
		t.Run(change, func(t *testing.T) {
			m, db, i, b, f := fixture(t)
			switch change {
			case "unlabelled":
				if _, err := db.Exec(`DELETE FROM workspace_operation_runs`); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				i.Epoch = strings.Repeat("2", 32)
			case "owner":
				b.Lease.Token = strings.Repeat("3", 32)
			case "scope":
				b.Lease.Execution.Root = "/another"
			case "unrevoked":
				b.Lease.StopRequested = false
			case "cooperative":
				if _, err := db.Exec(`UPDATE workspace_operations SET control_state='',control_root='',control_host='',control_boot=''`); err != nil {
					t.Fatal(err)
				}
			case "changed_history":
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				r, err := history.Get(tx, b.Lease.Execution.ID)
				if err != nil {
					t.Fatal(err)
				}
				r.Revision++
				if err = history.Save(tx, r); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.ReconcileInterruption(i); err == nil || f.count != 0 {
				t.Fatal("unowned scope fenced/observed", f.count, err)
			}
			r, err := history.Get(db, b.Lease.Execution.ID)
			if err != nil || r.Interruption != nil || !r.SealedAt.IsZero() {
				t.Fatal(r, err)
			}
		})
	}
}

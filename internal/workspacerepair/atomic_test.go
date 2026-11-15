package workspacerepair_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/initrepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
)

func (f *fixture) seedFullRetainedHistory() {
	f.t.Helper()
	must(f.t, f.update(func(rows benchstate.Database, tx *sql.Tx) error {
		b := rows["card"]
		for i := 0; i < model.WorkspaceRetainedRuns; i++ {
			id := fmt.Sprintf("%032x", 256+i)
			raw := []byte(fmt.Sprintf("preserved stage %d", i))
			if err := os.WriteFile(filepath.Join(f.store.State, "run-"+id+".log"), raw, 0600); err != nil {
				return err
			}
			if err := history.Start(tx, history.Record{ID: id, Origin: "supervised", Key: "card", Holder: "initialization", StartedAt: time.Now().UTC()}); err != nil {
				return err
			}
			entry, err := retention.Snapshot(f.store.State, id)
			if err != nil {
				return err
			}
			exit := 0
			if err := history.Seal(tx, id, entry, int64(len(raw)), &exit, false); err != nil {
				return err
			}
			b.Lease.RetainedRuns = append(b.Lease.RetainedRuns, id)
		}
		return nil
	}))
}

func TestFullRetainedInventoryAndFinalHistoryJournalFailureRollbackTogether(t *testing.T) {
	for _, failure := range []string{"history", "journal"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t, nil)
			f.seedFullRetainedHistory()
			f.stop()
			p := f.inspect()
			owner := f.claim(p, "repair1")
			claimed := f.row()
			if len(claimed.Lease.RetainedRuns) != 0 || len(claimed.Workspace.InitializationRepair.RetiredRuns) != 64 {
				t.Fatal("repair retained capacity deadlock or lost inventory")
			}
			trigger := "CREATE TRIGGER fail_finalization BEFORE UPDATE OF status ON initialization_repairs WHEN NEW.status='completed' BEGIN SELECT RAISE(ABORT,'injected final journal failure'); END"
			if failure == "history" {
				trigger = "CREATE TRIGGER fail_finalization BEFORE UPDATE ON run_history WHEN NEW.id='" + fmt.Sprintf("%032x", 319) + "' AND json_extract(NEW.record,'$.ReleasedAt') != '0001-01-01T00:00:00Z' BEGIN SELECT RAISE(ABORT,'injected final history failure'); END"
			}
			must(t, f.sql(func(tx *sql.Tx) error { _, err := tx.Exec(trigger); return err }))
			out, err := f.m.Continue(context.Background(), "card", "repair1", owner.GrantToken)
			if err == nil || out == nil || out.Phase != "prepared" || f.row().Lease == nil || f.row().Workspace.Checkpoint == nil {
				t.Fatal("failed final transaction fabricated readiness or lost owned checkpoint", err)
			}
			checkpoint := f.row().Workspace.Checkpoint.Seal
			records, err := f.store.History("card", 100)
			must(t, err)
			if len(records) != 64 {
				t.Fatal("repair discarded retained history")
			}
			for _, r := range records {
				if !r.ReleasedAt.IsZero() {
					t.Fatal("partial history release escaped failed final transaction")
				}
			}
			must(t, f.sql(func(tx *sql.Tx) error {
				j, err := initrepair.Get(tx, claimed.Workspace.ID, "repair1")
				if err != nil {
					return err
				}
				if j.Status != "active" {
					t.Fatal("failed final transaction completed repair journal")
				}
				_, err = tx.Exec("DROP TRIGGER fail_finalization")
				return err
			}))
			out, err = f.m.Continue(context.Background(), "card", "repair1", owner.GrantToken)
			must(t, err)
			if out.Phase != "ready" || f.row().Lease != nil || f.row().Workspace.Checkpoint.Seal != checkpoint {
				t.Fatal("exact final retry did not preserve checkpoint and publish readiness")
			}
			records, err = f.store.History("card", 100)
			must(t, err)
			for _, r := range records {
				if r.ReleasedAt.IsZero() || r.Checkpoint != checkpoint {
					t.Fatal("whole retained history did not release against exact genesis")
				}
				if _, err := os.Stat(filepath.Join(f.store.State, "run-"+r.ID+".log")); err != nil {
					t.Fatal("repair deleted retained evidence", err)
				}
			}
		})
	}
}

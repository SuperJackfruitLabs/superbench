package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunHistoryAndApprovedCollection(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	s.executor = &testExecutor{}
	run, e := s.Run(context.Background(), "task", "worker", []string{"sh", "-c", "printf evidence"}, true)
	if e != nil {
		t.Fatal(e)
	}
	records, e := s.History("task", 100)
	if e != nil || len(records) != 1 || records[0].ReleasedAt.IsZero() || records[0].File == nil || records[0].ExitCode == nil || *records[0].ExitCode != 0 {
		t.Fatal("run ownership/outcome not durable", e)
	}
	p, e := s.PlanCollection(0, 0, 100)
	if e != nil || len(p.Logs) != 1 {
		t.Fatal("completed log not eligible", e)
	}
	s.collectAfterDelete = func() error { return errors.New("crash") }
	if _, e = s.Collect(*p); e == nil {
		t.Fatal("injected crash ignored")
	}
	s.collectAfterDelete = nil
	c, e := s.ResumeCollection(p.Digest, false)
	if e != nil || c.Status != "completed" {
		t.Fatal("collection did not resume", e)
	}
	if _, e = os.Stat(filepath.Join(s.State, "run-"+run.Execution.ID+".log")); !os.IsNotExist(e) {
		t.Fatal("completed log not removed")
	}
	if _, e = s.History("task", 100); e != nil {
		t.Fatal(e)
	}
}
func TestFailedHistoryLogWaitsForRelease(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	s.executor = &testExecutor{}
	result, e := s.Run(context.Background(), "task", "worker", []string{"sh", "-c", "exit 7"}, true)
	if e == nil {
		t.Fatal("failure lost")
	}
	p, e := s.PlanCollection(0, 0, 100)
	if e != nil || len(p.Logs) != 0 {
		t.Fatal("failed held log was collectable", e)
	}
	if _, e = s.Checkpoint("task", result.Bench.Lease.Token); e != nil {
		t.Fatal(e)
	}
	if e = s.Release("task", result.Bench.Lease.Token); e != nil {
		t.Fatal(e)
	}
	p, e = s.PlanCollection(0, 0, 100)
	if e != nil || len(p.Logs) != 1 {
		t.Fatal("explicit reconciliation did not permit retention", e)
	}
}

func TestUnsealedReleasedLogRequiresWriterClosureBeforeReconciliation(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	s.executor = &testExecutor{startFailure: true}
	failed, e := s.Run(context.Background(), "task", "worker", []string{"true"}, true)
	if e == nil {
		t.Fatal("start failure missing")
	}
	if _, e = s.Stop("task", failed.Bench.Lease.Token, false); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Checkpoint("task", failed.Bench.Lease.Token); e != nil {
		t.Fatal(e)
	}
	if e = s.Release("task", failed.Bench.Lease.Token); e != nil {
		t.Fatal(e)
	}
	p, e := s.PlanCollection(0, 0, 100)
	if e != nil || len(p.Logs) != 0 {
		t.Fatal("unsealed failure was collected", e)
	}
	sealed, e := s.ReconcileLog(failed.Execution.ID)
	if e != nil || sealed.File == nil || sealed.ExitCode != nil {
		t.Fatal("reconciliation invented a command outcome", e)
	}
	p, e = s.PlanCollection(0, 0, 100)
	if e != nil || len(p.Logs) != 1 {
		t.Fatal("reconciled log unavailable for review", e)
	}
}

func TestHistoryCapacityRefusesBeforeCommandAndRollsBackLease(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	s.executor = &testExecutor{}
	db, e := s.openSQL(false)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	statement, e := tx.Prepare("INSERT INTO run_history(id,record) VALUES(?,?)")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < history.MaxRecords; i++ {
		id := fmt.Sprintf("%032x", i)
		record := history.Record{ID: id, Origin: "supervised", Key: "old", Holder: "worker", StartedAt: time.Now().UTC()}
		raw, e := json.Marshal(record)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = statement.Exec(id, string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	statement.Close()
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(s.State, "must-not-start")
	if _, e = s.Run(context.Background(), "task", "worker", []string{"touch", marker}, true); e == nil {
		t.Fatal("history capacity ignored")
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("command started despite exhausted history")
	}
	states, e := s.Status("task")
	if e != nil || states[0].Lease != nil {
		t.Fatal("history refusal left a new lease", e)
	}
}

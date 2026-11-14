package bench

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparationHandoffAndStopCancellation(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stop-between-stages"}[stop], func(t *testing.T) {
			s, remote := fixture(t)
			if _, e := s.Create("task", Options{Repo: remote}, false); e != nil {
				t.Fatal(e)
			}
			s.executor = &testExecutor{}
			preparing := true
			first, e := s.runStage(context.Background(), "task", "owner", []string{"sh", "-c", "printf setup"}, false, nil, stageOptions{preparing: &preparing})
			if e != nil {
				t.Fatal(e)
			}
			p := permit(first.Bench)
			if p.Continuation == "" || publicLease(first.Bench.Lease).Continuation != "" {
				t.Fatal("missing or public continuation authority")
			}
			if _, e = s.Lease("task", "competitor"); e == nil {
				t.Fatal("lease gap after preparation")
			}
			if stop {
				if _, e = s.Stop("task", "", true); e != nil {
					t.Fatal(e)
				}
			}
			marker := filepath.Join(t.TempDir(), "main-ran")
			second, e := s.runStage(context.Background(), "task", "owner", []string{"touch", marker}, false, nil, stageOptions{prior: p})
			if stop {
				if e == nil {
					t.Fatal("stopped continuation launched main")
				}
				if _, e = os.Stat(marker); !os.IsNotExist(e) {
					t.Fatal("main executed after successful stop")
				}
				return
			}
			if e != nil || second.Bench.Lease.Token != first.Bench.Lease.Token || len(second.Bench.Lease.RetainedRuns) != 1 {
				t.Fatal(second, e)
			}
			if _, e = os.Stat(marker); e != nil {
				t.Fatal(e)
			}
			plan, e := s.PlanCollection(0, 0, 100)
			if e != nil || len(plan.Logs) != 0 {
				t.Fatal("held preparation evidence collectible", e)
			}
			if _, e = s.Checkpoint("task", p.Token); e != nil {
				t.Fatal(e)
			}
			if _, e = s.Stop("task", "", true); e != nil {
				t.Fatal(e)
			}
			if e = s.release("task", p.Token, true); e == nil {
				t.Fatal("automatic completion released an operator-stopped lease")
			}
			if e = s.Release("task", p.Token); e != nil {
				t.Fatal(e)
			}
			history, e := s.History("task", 100)
			if e != nil || len(history) != 2 {
				t.Fatal(history, e)
			}
			for _, r := range history {
				if r.ReleasedAt.IsZero() {
					t.Fatal("preparation history was not released atomically")
				}
			}
		})
	}
}

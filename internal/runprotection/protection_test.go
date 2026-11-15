package runprotection

import (
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func TestRetiredMissingSourceEvidenceStaysProtectedAfterArchiveAndReap(t *testing.T) {
	for _, phase := range []string{"ready", "archived", "reaping", "reaped"} {
		b := &model.Bench{Workspace: &model.Workspace{Phase: phase, Transfer: &model.WorkspaceTransfer{Recovery: &model.WorkspaceTransferRecovery{Runs: []model.WorkspaceRetiredRun{{ID: "retired-current"}, {ID: "retired-dependency"}}}}}}
		ids := Runs(map[string]*model.Bench{"source": b, "nil": nil})
		if !ids["retired-current"] || !ids["retired-dependency"] || len(ids) != 2 {
			t.Fatal("retirement lost history protection", phase)
		}
	}
}
func TestOrdinaryAndInitializationRetentionKeepDistinctLifetimes(t *testing.T) {
	b := &model.Bench{Lease: &model.Lease{RetainedRuns: []string{"retained"}, Execution: &model.Execution{ID: "current"}}, Workspace: &model.Workspace{Phase: "initializing", InitializationRepair: &model.WorkspaceInitializationRepair{RetiredRuns: []string{"initialization-retired"}}}}
	ids := Runs(map[string]*model.Bench{"held": b})
	if len(ids) != 3 || !ids["current"] || !ids["retained"] || !ids["initialization-retired"] {
		t.Fatal("held history not protected")
	}
	b.Lease = nil
	b.Workspace.Phase = "ready"
	if len(Runs(map[string]*model.Bench{"ready": b})) != 0 {
		t.Fatal("completed initialization marker permanently pinned released history")
	}
}

package main

import (
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func TestWorkspaceCloneOwnsMissingTransferRetirementEvidence(t *testing.T) {
	original := &model.Bench{Workspace: &model.Workspace{Transfer: &model.WorkspaceTransfer{Recovery: &model.WorkspaceTransferRecovery{Mode: "missing", Execution: &model.Execution{ID: "original", HostID: "source-host"}, Stage: &model.WorkspaceStage{Name: "api", Digest: "stage"}, Runs: []model.WorkspaceRetiredRun{{ID: "run", Digest: "history"}}}}}}
	copy := workspace.Clone(original)
	if copy == original || copy.Workspace == original.Workspace || copy.Workspace.Transfer == original.Workspace.Transfer || copy.Workspace.Transfer.Recovery == original.Workspace.Transfer.Recovery {
		t.Fatal("clone aliases missing retirement authority")
	}
	copy.Workspace.Transfer.Recovery.Mode = "changed"
	copy.Workspace.Transfer.Recovery.Execution.HostID = "changed"
	copy.Workspace.Transfer.Recovery.Stage.Digest = "changed"
	copy.Workspace.Transfer.Recovery.Runs[0].Digest = "changed"
	r := original.Workspace.Transfer.Recovery
	if r.Mode != "missing" || r.Execution.HostID != "source-host" || r.Stage.Digest != "stage" || r.Runs[0].Digest != "history" {
		t.Fatal("clone leaked mutable scope, stage or run evidence")
	}
	original.Workspace.Transfer.Recovery.Runs = []model.WorkspaceRetiredRun{}
	if workspace.Clone(original).Workspace.Transfer.Recovery.Runs == nil {
		t.Fatal("clone changed empty approved run vector into null")
	}
	original.Workspace.Transfer.Recovery.Runs = []model.WorkspaceRetiredRun{{ID: "run", Digest: "history"}}

}

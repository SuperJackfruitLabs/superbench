package workspacepolicy

import (
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func missingTransfer(t *testing.T) *model.Bench {
	b := transferRecord(t)
	b.Workspace.Transfer.Recovery = &model.WorkspaceTransferRecovery{Mode: "missing", SourceRowDigest: strings.Repeat("1", 64), RetiredAt: time.Now().UTC().Format(time.RFC3339Nano)}
	return b
}
func TestMissingTransferRecoveryHasExplicitSafeEvidenceAndHistoricalBarrier(t *testing.T) {
	b := missingTransfer(t)
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	if ValidateSchema21(b) == nil {
		t.Fatal("schema21 adopted missing retirement")
	}
	for name, change := range map[string]func(*model.WorkspaceTransferRecovery){
		"mode":       func(r *model.WorkspaceTransferRecovery) { r.Mode = "present" },
		"row-digest": func(r *model.WorkspaceTransferRecovery) { r.SourceRowDigest = "" },
		"time":       func(r *model.WorkspaceTransferRecovery) { r.RetiredAt = "2026-11-15T12:00:00+01:00" },
		"duplicate-runs": func(r *model.WorkspaceTransferRecovery) {
			r.Runs = []model.WorkspaceRetiredRun{{ID: strings.Repeat("a", 32), Digest: strings.Repeat("b", 64)}, {ID: strings.Repeat("a", 32), Digest: strings.Repeat("b", 64)}}
		},
		"run-digest": func(r *model.WorkspaceTransferRecovery) {
			r.Runs = []model.WorkspaceRetiredRun{{ID: strings.Repeat("a", 32), Digest: strings.Repeat("B", 64)}}
		},
		"unowned-scope": func(r *model.WorkspaceTransferRecovery) { r.Execution = executionRecord() },
		"stage-without-scope": func(r *model.WorkspaceTransferRecovery) {
			r.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("b", 64)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := missingTransfer(t)
			change(b.Workspace.Transfer.Recovery)
			if Validate(b) == nil {
				t.Fatal("malformed retired authority accepted")
			}
		})
	}
}

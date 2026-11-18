package workspacepolicy

import (
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// Only a create that never completed, with no run, checkpoint, transfer or
// repair, is unfinished; every other record is refused before root is asked.
// Each case is a valid record, so only Unfinished's own guard refuses it.
func TestOnlyACreateThatNeverCompletedIsUnfinished(t *testing.T) {
	initializing := func() *model.Bench {
		b := validRecord()
		b.Workspace.Phase, b.ProjectID, b.QuotaBytes, b.QuotaInodes = "initializing", 10009, 4096, 128
		return b
	}
	for name, b := range map[string]*model.Bench{"materializing": validRecord(), "initializing": initializing()} {
		if !Unfinished(b) {
			t.Errorf("%s is not unfinished", name)
		}
	}
	for name, make := range map[string]func() *model.Bench{
		"prepared": prepared,
		"ready with a writer": func() *model.Bench {
			b := ReleasedRecord(t)
			b.Lease = &model.Lease{Holder: "worker", Token: strings.Repeat("a", 48), Continuation: strings.Repeat("b", 32)}
			return b
		},
		"a run": func() *model.Bench {
			b := initializing()
			b.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("7", 64)}
			b.Lease.Execution = executionRecord()
			return b
		},
		"a pending checkpoint": func() *model.Bench {
			b := initializing()
			b.Workspace.Pending = checkpoint(t, b)
			return b
		},
		"a checkpoint": func() *model.Bench {
			b := initializing()
			b.Workspace.Checkpoint = checkpoint(t, b)
			b.Workspace.Checkpoint.Seal = strings.Repeat("f", 40)
			return b
		},
		"transferred": func() *model.Bench {
			b := validRecord()
			b.Workspace.TransferredFrom = &model.WorkspaceTransferOrigin{ID: "transfer1", Digest: strings.Repeat("7", 64), SourceKey: "source", SourceID: strings.Repeat("8", 32)}
			return b
		},
		"a repair": func() *model.Bench {
			b := initializing()
			b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "repair1", Digest: strings.Repeat("7", 64)}
			return b
		},
		"no workspace": func() *model.Bench { b := validRecord(); b.Workspace = nil; return b },
		"invalid":      func() *model.Bench { b := validRecord(); b.Workspace.ID = "card"; return b },
	} {
		b := make()
		if err := Validate(b); err != nil && name != "invalid" && name != "no workspace" {
			t.Fatalf("%s: the case is not a valid record: %v", name, err)
		}
		if Unfinished(b) {
			t.Errorf("%s: unfinished", name)
		}
	}
	if Unfinished(nil) {
		t.Fatal("nil unfinished")
	}
	t.Log("FAILED_CREATE_ONLY_UNFINISHED_RECORDS_ASK_ROOT_PASS")
}

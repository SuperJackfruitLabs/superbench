package workspacepolicy

import (
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// stopped is the daemon's stop of b's initialization owner, as stop and
// stop_admin leave it.
func stopped(b *model.Bench) *model.Bench {
	b.Lease.StopRequested, b.Lease.Continuation = true, ""
	if b.Lease.Execution != nil {
		b.Lease.Execution.Phase = "stopped"
	}
	return b
}

func allocated(b *model.Bench, phase string) *model.Bench {
	b.Workspace.Phase, b.ProjectID, b.QuotaBytes, b.QuotaInodes = phase, 10009, 4096, 128
	return b
}

// Only a materializing record with no project is unfinished: its bootstrap
// never completed, so only root's direct recovery can have retired it. Only a
// record whose bootstrap completed, whose initialization the daemon stopped,
// with no live run and nothing checkpointed, is stalled. Each refused case is
// a valid record, so only the predicate's own guard refuses it.
func TestUnfinishedAndStalledCreatesAreTheOnlyOnesRootIsAsked(t *testing.T) {
	cases := map[string]struct {
		make                func() *model.Bench
		unfinished, stalled bool
	}{
		"materializing":                {validRecord, true, false},
		"stopped materializing":        {func() *model.Bench { return stopped(validRecord()) }, true, false},
		"materializing with a project": {func() *model.Bench { return allocated(validRecord(), "materializing") }, false, false},
		"stopped, with a project":      {func() *model.Bench { return stopped(allocated(validRecord(), "materializing")) }, false, true},
		"initializing":                 {func() *model.Bench { return allocated(validRecord(), "initializing") }, false, false},
		"stopped initializing":         {func() *model.Bench { return stopped(allocated(validRecord(), "initializing")) }, false, true},
		"initializing without a project": {func() *model.Bench {
			b := stopped(validRecord())
			b.Workspace.Phase = "initializing"
			return b
		}, false, false},
		"stop requested, continuation kept": {func() *model.Bench {
			b := allocated(validRecord(), "initializing")
			b.Lease.StopRequested = true
			return b
		}, false, false},
		"a stopped run": {func() *model.Bench {
			b := allocated(validRecord(), "initializing")
			b.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("7", 64)}
			b.Lease.Execution = executionRecord()
			return stopped(b)
		}, false, true},
		"a live run": {func() *model.Bench {
			b := stopped(allocated(validRecord(), "initializing"))
			b.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("7", 64)}
			b.Lease.Execution = executionRecord()
			b.Lease.Execution.Phase, b.Lease.Execution.PID = "running", 4242
			return b
		}, false, false},
		"stopped and prepared": {func() *model.Bench { return stopped(prepared()) }, false, false},
		"a pending checkpoint": {func() *model.Bench {
			b := stopped(allocated(validRecord(), "initializing"))
			b.Workspace.Pending = checkpoint(t, b)
			return b
		}, false, false},
		"a checkpoint": {func() *model.Bench {
			b := stopped(allocated(validRecord(), "initializing"))
			b.Workspace.Checkpoint = checkpoint(t, b)
			b.Workspace.Checkpoint.Seal = strings.Repeat("f", 40)
			return b
		}, false, false},
		"transferred": {func() *model.Bench {
			b := validRecord()
			b.Workspace.TransferredFrom = &model.WorkspaceTransferOrigin{ID: "transfer1", Digest: strings.Repeat("7", 64), SourceKey: "source", SourceID: strings.Repeat("8", 32)}
			return b
		}, false, false},
		"transferred and stopped": {func() *model.Bench {
			b := stopped(allocated(validRecord(), "initializing"))
			b.Workspace.TransferredFrom = &model.WorkspaceTransferOrigin{ID: "transfer1", Digest: strings.Repeat("7", 64), SourceKey: "source", SourceID: strings.Repeat("8", 32)}
			return b
		}, false, false},
		"a repair": {func() *model.Bench {
			b := allocated(validRecord(), "initializing")
			b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "repair1", Digest: strings.Repeat("7", 64)}
			return stopped(b)
		}, false, false},
		"a materializing repair": {func() *model.Bench {
			b := validRecord()
			b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "repair1", Digest: strings.Repeat("7", 64), OperationDigest: strings.Repeat("6", 64)}
			return b
		}, false, false},
		"ready with a writer": {func() *model.Bench {
			b := ReleasedRecord(t)
			b.Lease = &model.Lease{Holder: "worker", Token: strings.Repeat("a", 48), StopRequested: true}
			return b
		}, false, false},
	}
	for name, c := range cases {
		b := c.make()
		if err := Validate(b); err != nil {
			t.Fatalf("%s: the case is not a valid record: %v", name, err)
		}
		if Unfinished(b) != c.unfinished || Stalled(b) != c.stalled {
			t.Errorf("%s: unfinished %v stalled %v", name, Unfinished(b), Stalled(b))
		}
	}
	invalid := stopped(allocated(validRecord(), "initializing"))
	invalid.Workspace.ID = "card"
	if Unfinished(invalid) || Stalled(invalid) || Unfinished(nil) || Stalled(nil) || Stalled(&model.Bench{}) {
		t.Fatal("an invalid record is unfinished or stalled")
	}
	t.Log("STUCK_CREATE_ONLY_UNFINISHED_OR_STALLED_RECORDS_ASK_ROOT_PASS")
}

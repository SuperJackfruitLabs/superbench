package workspacepolicy

import (
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// RemoteRecord is a ready workspace leased to a worker whose remote writer
// execution is running. Exported for the transition test outside the package.
func RemoteRecord(t *testing.T) *model.Bench {
	t.Helper()
	b := ReleasedRecord(t)
	b.Lease = &model.Lease{ID: strings.Repeat("5", 32), Generation: 1, Holder: "worker", Token: strings.Repeat("a", 48), Continuation: strings.Repeat("b", 32),
		Execution: &model.Execution{ID: strings.Repeat("7", 32), Root: "/sys/fs/cgroup/superbench", BootID: "test-boot", HostID: "test-host", Phase: "running", Kind: "remote"}}
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRemoteExecutionRecordShape(t *testing.T) {
	stopped := RemoteRecord(t)
	stopped.Lease.Execution.Phase = "stopped"
	stopped.Lease.StopRequested, stopped.Lease.Continuation = true, ""
	if err := Validate(stopped); err != nil {
		t.Fatal("stopped remote execution refused", err)
	}
	for name, change := range map[string]func(*model.Bench){
		"unknown-kind":  func(b *model.Bench) { b.Lease.Execution.Kind = "shell" },
		"log":           func(b *model.Bench) { b.Lease.Execution.Log = "/srv/state/run.log" },
		"log-limit":     func(b *model.Bench) { b.Lease.Execution.LogLimitBytes = 1 << 20 },
		"pid":           func(b *model.Bench) { b.Lease.Execution.PID = 42 },
		"starting":      func(b *model.Bench) { b.Lease.Execution.Phase = "starting" },
		"no-phase":      func(b *model.Bench) { b.Lease.Execution.Phase = "" },
		"bad-id":        func(b *model.Bench) { b.Lease.Execution.ID = "../scope" },
		"relative-root": func(b *model.Bench) { b.Lease.Execution.Root = "cgroup" },
		"long-boot":     func(b *model.Bench) { b.Lease.Execution.BootID = strings.Repeat("b", 129) },
		"long-host":     func(b *model.Bench) { b.Lease.Execution.HostID = strings.Repeat("h", 129) },
		"stage": func(b *model.Bench) {
			b.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("1", 64)}
		},
		"initialization-holder": func(b *model.Bench) { b.Lease.Holder = "initialization" },
		"not-ready": func(b *model.Bench) {
			b.Workspace.Phase = "prepared"
			b.Workspace.Checkpoint = nil
		},
		"retained-remote": func(b *model.Bench) {
			b.Lease.RetainedRuns = []string{strings.Repeat("8", 32)}
			b.Lease.Execution.ID = b.Lease.RetainedRuns[0]
		},
	} {
		b := RemoteRecord(t)
		change(b)
		if Validate(b) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A run execution keeps every rule it had: a kind is never required.
	run := RemoteRecord(t)
	run.Lease.Execution = executionRecord()
	if err := Validate(run); err != nil {
		t.Fatal("run execution refused", err)
	}
	run.Lease.Execution.Kind = "run"
	if Validate(run) == nil {
		t.Fatal("unknown kind on a run accepted")
	}
}

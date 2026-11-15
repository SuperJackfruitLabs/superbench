package workspacepolicy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func checkpoint(t *testing.T, b *model.Bench) *model.WorkspaceCheckpoint {
	t.Helper()
	c := &model.WorkspaceCheckpoint{GenerationID: strings.Repeat("e", 32)}
	if b.Workspace.Checkpoint != nil {
		c.Previous = b.Workspace.Checkpoint.Seal
	}
	for _, m := range b.Workspace.Members {
		c.Members = append(c.Members, model.WorkspaceCommit{Name: m.Name, Commit: m.Source})
	}
	m, err := Manifest(b, c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	c.Digest = checkpointset.Digest(raw)
	return c
}
func prepared() *model.Bench {
	b := validRecord()
	b.Workspace.Phase = "prepared"
	b.Workspace.Preparation = []model.WorkspacePreparation{{Name: "api", Digest: strings.Repeat("1", 64)}}
	return b
}
func executionRecord() *model.Execution {
	return &model.Execution{ID: strings.Repeat("2", 32), Root: "/sys/fs/cgroup/workspace", BootID: "test-boot", HostID: "test-host", Phase: "stopped", Log: "/srv/state/run.log", LogLimitBytes: 1 << 20}
}

func TestManifestDigestAndWholeCheckpointValidation(t *testing.T) {
	b := prepared()
	p := checkpoint(t, b)
	if p.Digest == "" {
		t.Fatal("missing digest")
	}
	b.Workspace.Pending = p
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	b.Workspace.Checkpoint = p
	b.Workspace.Pending = nil
	p.Seal = strings.Repeat("f", 40)
	b.Workspace.Phase = "ready"
	b.Lease = nil
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	p2 := checkpoint(t, b)
	p2.GenerationID = strings.Repeat("a", 32)
	m, err := Manifest(b, p2)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	p2.Digest = checkpointset.Digest(raw)
	b.Workspace.Pending = p2
	b.Lease = &model.Lease{Holder: "worker", Token: strings.Repeat("b", 32)}
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
}
func TestLifecycleRejectsIncompleteOrAlteredEvidence(t *testing.T) {
	tests := map[string]func(*model.Bench){
		"missing-preparation": func(b *model.Bench) { b.Workspace.Preparation = nil },
		"unknown-preparation": func(b *model.Bench) { b.Workspace.Preparation[0].Name = "unknown" },
		"bad-fingerprint":     func(b *model.Bench) { b.Workspace.Preparation[0].Digest = "bad" },
		"unretained-proof":    func(b *model.Bench) { b.Workspace.Preparation[0].RunID = strings.Repeat("9", 32) },
		"missing-vector":      func(b *model.Bench) { b.Workspace.Pending.Members = nil },
		"wrong-name":          func(b *model.Bench) { b.Workspace.Pending.Members[0].Name = "other" },
		"wrong-format":        func(b *model.Bench) { b.Workspace.Pending.Members[0].Commit = strings.Repeat("a", 64) },
		"wrong-digest":        func(b *model.Bench) { b.Workspace.Pending.Digest = strings.Repeat("0", 64) },
		"sealed-pending":      func(b *model.Bench) { b.Workspace.Pending.Seal = strings.Repeat("a", 40) },
		"pending-previous":    func(b *model.Bench) { b.Workspace.Pending.Previous = strings.Repeat("a", 40) },
		"pending-running": func(b *model.Bench) {
			b.Lease.Execution = executionRecord()
			b.Lease.Execution.Phase = "running"
			b.Lease.Execution.PID = 123
		},
		"scope-root": func(b *model.Bench) {
			b.Workspace.Pending = nil
			b.Lease.Execution = executionRecord()
			b.Lease.Execution.Root = "relative"
		},
		"scope-log": func(b *model.Bench) {
			b.Workspace.Pending = nil
			b.Lease.Execution = executionRecord()
			b.Lease.Execution.Log = "/"
		},
		"scope-host": func(b *model.Bench) {
			b.Workspace.Pending = nil
			b.Lease.Execution = executionRecord()
			b.Lease.Execution.HostID = ""
		},
		"scope-id": func(b *model.Bench) {
			b.Workspace.Pending = nil
			b.Lease.Execution = executionRecord()
			b.Lease.Execution.ID = "bad"
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			b := prepared()
			b.Workspace.Pending = checkpoint(t, b)
			change(b)
			if err := Validate(b); err == nil {
				t.Fatal("invalid lifecycle state accepted")
			}
		})
	}
}
func TestInitializationSupports64RetainedRunsAndStoppedProof(t *testing.T) {
	b := validRecord()
	b.Workspace.Phase = "initializing"
	for i := 0; i < model.WorkspaceRetainedRuns; i++ {
		b.Lease.RetainedRuns = append(b.Lease.RetainedRuns, fmt.Sprintf("%032x", i+1))
	}
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	b.Lease.RetainedRuns = append(b.Lease.RetainedRuns, strings.Repeat("f", 32))
	if err := Validate(b); err == nil {
		t.Fatal("retained chain exceeded64")
	}
	b.Lease.RetainedRuns = nil
	b.Lease.Execution = executionRecord()
	b.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("1", 64)}
	b.Workspace.Preparation = []model.WorkspacePreparation{{Name: "api", Digest: strings.Repeat("1", 64), RunID: b.Lease.Execution.ID}}
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	b.Lease.StopRequested = true
	b.Lease.Continuation = ""
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	b.Lease.StopRequested = false
	if err := Validate(b); err == nil {
		t.Fatal("initialization lost continuation without stop intent")
	}
}

func TestDurableStageRequiresMatchingInitializationScope(t *testing.T) {
	b := validRecord()
	b.Workspace.Phase = "initializing"
	b.Lease.Execution = executionRecord()
	if err := Validate(b); err == nil {
		t.Fatal("initialization execution has no durable member stage")
	}
	b.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("1", 64)}
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	b.Workspace.Stage.Name = "unknown"
	if err := Validate(b); err == nil {
		t.Fatal("unknown member stage accepted")
	}
	b.Workspace.Stage.Name = "api"
	b.Lease.Execution = nil
	if err := Validate(b); err == nil {
		t.Fatal("stage without execution accepted")
	}
	b.Lease.Execution = executionRecord()
	b.Workspace.Preparation = []model.WorkspacePreparation{{Name: "api", Digest: strings.Repeat("1", 64)}}
	b.Workspace.Phase = "prepared"
	if err := Validate(b); err == nil {
		t.Fatal("prepared record kept active stage intent")
	}
}

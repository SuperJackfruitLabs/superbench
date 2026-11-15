package benchstate

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func stoppedInitialization() *model.Bench {
	b := preparedAggregate()
	b.Workspace.Phase = "initializing"
	b.Lease.Continuation = ""
	b.Lease.StopRequested = true
	b.Lease.RetainedRuns = []string{strings.Repeat("1", 32)}
	b.Workspace.Preparation[0].RunID = b.Lease.RetainedRuns[0]
	b.Lease.Execution = trackedExecution()
	b.Lease.Execution.Phase = "stopped"
	b.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("2", 64)}
	return b
}
func cloneBench(t *testing.T, b *model.Bench) *model.Bench {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var c model.Bench
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return &c
}
func repairClaim(t *testing.T, b *model.Bench) *model.Bench {
	c := cloneBench(t, b)
	c.Lease.Token = strings.Repeat("4", 32)
	c.Lease.Continuation = strings.Repeat("5", 32)
	c.Lease.StopRequested = false
	c.Lease.Execution = nil
	c.Workspace.Stage = nil
	c.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "repair1", Digest: strings.Repeat("6", 64), RetiredRuns: []string{b.Lease.Execution.ID}, UncertainOnce: []string{"api"}}
	return c
}
func assertRepairValid(t *testing.T, old, next *model.Bench) {
	t.Helper()
	if err := Validate(Database{"card": next}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTransition(Capture(Database{"card": old}), Database{"card": next}); err != nil {
		t.Fatal(err)
	}
}
func TestInitializationRepairReplacesOnlyStoppedOwnerAndRetainsInventory(t *testing.T) {
	old := stoppedInitialization()
	next := repairClaim(t, old)
	assertRepairValid(t, old, next)
	// Only exact known stopped stage evidence may be salvaged into preparation.
	salvaged := repairClaim(t, old)
	salvaged.Workspace.Preparation[0] = model.WorkspacePreparation{Name: old.Workspace.Stage.Name, Digest: old.Workspace.Stage.Digest, RunID: old.Lease.Execution.ID}
	salvaged.Lease.RetainedRuns = []string{old.Lease.Execution.ID}
	salvaged.Workspace.InitializationRepair.RetiredRuns = append([]string(nil), old.Lease.RetainedRuns...)
	assertRepairValid(t, old, salvaged)
	// Ordinary progress cannot rebind the marker, nor silently revive ownership.
	before := Capture(Database{"card": next})
	next.Workspace.InitializationRepair.Digest = strings.Repeat("8", 64)
	if err := ValidateTransition(before, Database{"card": next}); err == nil {
		t.Fatal("ordinary progress rebound repair approval")
	}
}

func TestRepairClaimCannotBypassIdentityPendingHistoryOrProofGuards(t *testing.T) {
	changes := map[string]func(*model.Bench, *model.Bench){
		"live-old":             func(old, _ *model.Bench) { old.Lease.Execution.Phase = "running"; old.Lease.Execution.PID = 99 },
		"unrevoked-old":        func(old, _ *model.Bench) { old.Lease.Continuation = strings.Repeat("b", 32) },
		"unstopped-old":        func(old, _ *model.Bench) { old.Lease.StopRequested = false },
		"same-token":           func(old, next *model.Bench) { next.Lease.Token = old.Lease.Token },
		"missing-continuation": func(_, next *model.Bench) { next.Lease.Continuation = "" },
		"new-execution":        func(_, next *model.Bench) { next.Lease.Execution = trackedExecution() },
		"lost-run":             func(_, next *model.Bench) { next.Workspace.InitializationRepair.RetiredRuns = nil },
		"invented-run": func(_, next *model.Bench) {
			next.Workspace.InitializationRepair.RetiredRuns = append(next.Workspace.InitializationRepair.RetiredRuns, strings.Repeat("f", 32))
		},
		"unowned-proof": func(_, next *model.Bench) {
			next.Workspace.Preparation[0].RunID = strings.Repeat("f", 32)
			next.Lease.RetainedRuns = []string{strings.Repeat("f", 32)}
		},
		"invented-proof": func(_, next *model.Bench) { next.Workspace.Preparation[0].Digest = strings.Repeat("f", 64) },
		"quota":          func(_, next *model.Bench) { next.ProjectID = 9; next.QuotaBytes = 4096; next.QuotaInodes = 1024 },
		"identity":       func(_, next *model.Bench) { next.Workspace.ID = strings.Repeat("f", 32) },
		"checkpoint":     func(_, next *model.Bench) { next.Workspace.Checkpoint = &model.WorkspaceCheckpoint{Seal: "new"} },
		"ready":          func(_, next *model.Bench) { next.Workspace.Phase = "ready" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			old := stoppedInitialization()
			next := repairClaim(t, old)
			change(old, next)
			if err := ValidateTransition(Capture(Database{"card": old}), Database{"card": next}); err == nil {
				t.Fatal("repair claim bypassed narrow transition guard")
			}
		})
	}
}

func TestRepairClaimPreservesPendingVectorAndPreparation(t *testing.T) {
	old := stoppedInitialization()
	old.Workspace.Phase = "prepared"
	old.Workspace.Stage = nil
	old.Lease.Execution = nil
	old.Workspace.Pending = pendingCheckpoint(t, old)
	next := cloneBench(t, old)
	next.Lease.Token = strings.Repeat("4", 32)
	next.Lease.Continuation = strings.Repeat("5", 32)
	next.Lease.StopRequested = false
	next.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "repair1", Digest: strings.Repeat("6", 64)}
	assertRepairValid(t, old, next)
	before := Capture(Database{"card": old})
	for _, change := range []func(*model.Bench){func(b *model.Bench) { b.Workspace.Pending = nil }, func(b *model.Bench) { b.Workspace.Pending.GenerationID = strings.Repeat("a", 32) }, func(b *model.Bench) { b.Workspace.Preparation = nil }} {
		bad := cloneBench(t, next)
		change(bad)
		if err := ValidateTransition(before, Database{"card": bad}); err == nil {
			t.Fatal("repair replaced frozen pending evidence")
		}
	}
}

func TestRepeatedRepairPreservesRetiredRunsUncertaintyAndSnapshotCopies(t *testing.T) {
	old := repairClaim(t, stoppedInitialization())
	old.Lease.StopRequested = true
	old.Lease.Continuation = ""
	next := cloneBench(t, old)
	next.Lease.Token = strings.Repeat("8", 32)
	next.Lease.Continuation = strings.Repeat("9", 32)
	next.Lease.StopRequested = false
	next.Workspace.InitializationRepair.ID = "repair2"
	next.Workspace.InitializationRepair.PreviousID = "repair1"
	assertRepairValid(t, old, next)
	before := Capture(Database{"card": old})
	old.Workspace.InitializationRepair.RetiredRuns[0] = strings.Repeat("f", 32)
	old.Workspace.InitializationRepair.UncertainOnce[0] = "changed"
	if before["card"].repair.RetiredRuns[0] == old.Workspace.InitializationRepair.RetiredRuns[0] || before["card"].repair.UncertainOnce[0] == "changed" {
		t.Fatal("snapshot aliased mutable repair authority")
	}
	for _, change := range []func(*model.Bench){func(b *model.Bench) { b.Workspace.InitializationRepair.RetiredRuns = nil }, func(b *model.Bench) { b.Workspace.InitializationRepair.UncertainOnce = nil }, func(b *model.Bench) { b.Workspace.InitializationRepair.PreviousID = "wrong" }} {
		bad := cloneBench(t, next)
		change(bad)
		if err := ValidateTransition(before, Database{"card": bad}); err == nil {
			t.Fatal("repair forgot cumulative evidence")
		}
	}
}

func TestRepairRetiresFullLeaseInventoryWithoutReleasingOrLosingRuns(t *testing.T) {
	old := stoppedInitialization()
	old.Lease.RetainedRuns = nil
	for i := 0; i < model.WorkspaceRetainedRuns; i++ {
		old.Lease.RetainedRuns = append(old.Lease.RetainedRuns, fmt.Sprintf("%032x", 256+i))
	}
	old.Workspace.Preparation[0].RunID = old.Lease.RetainedRuns[0]
	next := repairClaim(t, old)
	next.Lease.RetainedRuns = []string{old.Lease.RetainedRuns[0]}
	next.Workspace.InitializationRepair.RetiredRuns = append(next.Workspace.InitializationRepair.RetiredRuns, old.Lease.RetainedRuns[1:]...)
	slices.Sort(next.Workspace.InitializationRepair.RetiredRuns)
	assertRepairValid(t, old, next)
	if len(next.Workspace.InitializationRepair.RetiredRuns) != 64 || len(next.Lease.RetainedRuns) != 1 {
		t.Fatal("full lease inventory was not partitioned")
	}
}

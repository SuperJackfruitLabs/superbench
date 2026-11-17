package workspacepolicy_test

import (
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// A released ready workspace moves to reclaiming and then reclaimed, never
// back, and never past a writer or uncheckpointed work.
func TestReclaimTransitionsAreOneWay(t *testing.T) {
	step := func(from *model.Bench, to string, change func(*model.Bench)) error {
		records := benchstate.Database{"card": from}
		before := benchstate.Capture(records)
		next := *from
		w := *from.Workspace
		next.Workspace = &w
		next.Workspace.Phase = to
		if change != nil {
			change(&next)
		}
		return benchstate.ValidateTransition(before, benchstate.Database{"card": &next})
	}
	ready := workspacepolicy.ReleasedRecord(t)
	if err := step(ready, "reclaiming", nil); err != nil {
		t.Fatal(err)
	}
	reclaiming := workspacepolicy.ReleasedRecord(t)
	reclaiming.Workspace.Phase = "reclaiming"
	if err := step(reclaiming, "reclaimed", nil); err != nil {
		t.Fatal(err)
	}
	reclaimed := workspacepolicy.ReleasedRecord(t)
	reclaimed.Workspace.Phase = "reclaimed"
	for name, err := range map[string]error{
		"ready-reclaimed":    step(workspacepolicy.ReleasedRecord(t), "reclaimed", nil),
		"reclaiming-ready":   step(reclaiming, "ready", nil),
		"reclaimed-ready":    step(reclaimed, "ready", nil),
		"reclaimed-again":    step(reclaimed, "reclaiming", nil),
		"reclaiming-reaped":  step(reclaiming, "reaped", nil),
		"reaping-reclaimed":  step(func() *model.Bench { b := workspacepolicy.ReleasedRecord(t); b.Workspace.Phase = "reaping"; return b }(), "reclaimed", nil),
		"with-pending":       step(ready, "reclaiming", func(b *model.Bench) { c := *b.Workspace.Checkpoint; b.Workspace.Pending = &c }),
		"with-archive":       step(ready, "reclaiming", func(b *model.Bench) { b.Workspace.Archive = &model.WorkspaceArchive{} }),
		"without-checkpoint": step(ready, "reclaiming", func(b *model.Bench) { b.Workspace.Checkpoint = nil }),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

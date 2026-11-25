package workspacepolicy

import (
	"slices"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// validRemote is the only shape a remote writer execution may take: a scope on
// a ready operational lease, with no process, log or history of its own.
func validRemote(b *model.Bench, e *model.Execution) bool {
	return e.Kind == execution.KindRemote && e.Log == "" && e.LogLimitBytes == 0 && e.PID == 0 &&
		(e.Phase == "running" || e.Phase == "stopped") && b.Workspace.Phase == "ready" && b.Workspace.Stage == nil && b.Lease.Holder != "initialization" &&
		identity.MatchString(e.ID) && physicalPath(e.Root) && boundedText(e.BootID, 128) && boundedText(e.HostID, 128)
}

// RemoteExecutionTransition decides a lease's execution change when either
// side has a kind. A grant records a running remote scope; a fence clears one
// in any phase; a replacement swaps a remote scope for a new running one. A
// remote scope never becomes, or replaces, a run and never enters retained
// history. decided is false when the ordinary rules own the change: neither
// side has a kind, or the same remote scope stays recorded.
func RemoteExecutionTransition(old, next *model.Execution, retained []string) (ok, decided bool) {
	kind := func(e *model.Execution) string {
		if e == nil {
			return ""
		}
		return e.Kind
	}
	if kind(old) == "" && kind(next) == "" {
		return false, false
	}
	if kind(old) != "" && kind(old) != execution.KindRemote || kind(next) != "" && kind(next) != execution.KindRemote || kind(old) != kind(next) && old != nil && next != nil {
		return false, true
	}
	if old != nil && slices.Contains(retained, old.ID) {
		return false, true
	}
	switch {
	case next == nil:
		return true, true
	case old == nil || old.ID != next.ID:
		return next.Phase == "running", true
	}
	return false, false
}

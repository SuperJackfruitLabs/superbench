package benchstate

import (
	"reflect"
	"slices"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func copyTransfer(t *model.WorkspaceTransfer) *model.WorkspaceTransfer {
	if t == nil {
		return nil
	}
	c := *t
	if c.CrossHost != nil {
		h := *c.CrossHost
		c.CrossHost = &h
	}
	if t.Recovery != nil {
		r := *t.Recovery
		r.Runs = slices.Clone(r.Runs)
		if r.Execution != nil {
			e := *r.Execution
			r.Execution = &e
		}
		if r.Stage != nil {
			s := *r.Stage
			r.Stage = &s
		}
		c.Recovery = &r
	}
	return &c
}

// retirementTransition is the sole exception to an outgoing source's already
// unleased precondition. It retains every old dependency without claiming release.
func retirementTransition(old identityRecord, r *model.WorkspaceTransferRecovery) bool {
	if r == nil {
		return old.lease == nil && old.stage == nil
	}
	if r.Mode != "missing" || r.SourceRowDigest != old.rowDigest {
		return false
	}
	if old.lease == nil {
		if r.Holder != "" || r.Execution != nil || r.Stage != nil || old.stage != nil {
			return false
		}
	} else {
		l := old.lease
		if !l.StopRequested || l.Continuation != "" || l.Execution != nil && l.Execution.Phase != "stopped" || r.Holder != l.Holder || !reflect.DeepEqual(r.Execution, l.Execution) || !reflect.DeepEqual(r.Stage, old.stage) {
			return false
		}
	}
	ids := map[string]bool{}
	if old.lease != nil {
		for _, id := range old.lease.RetainedRuns {
			ids[id] = true
		}
		if e := old.lease.Execution; e != nil {
			ids[e.ID] = true
		}
	}
	for _, p := range old.preparation {
		if p.RunID != "" {
			ids[p.RunID] = true
		}
	}
	if old.repair != nil {
		for _, id := range old.repair.RetiredRuns {
			ids[id] = true
		}
	}
	if len(ids) != len(r.Runs) {
		return false
	}
	for _, run := range r.Runs {
		if !ids[run.ID] {
			return false
		}
		delete(ids, run.ID)
	}
	return len(ids) == 0
}

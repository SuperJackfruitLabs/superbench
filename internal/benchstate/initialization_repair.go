package benchstate

import (
	"reflect"
	"slices"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func copyInitializationRepair(r *model.WorkspaceInitializationRepair) *model.WorkspaceInitializationRepair {
	if r == nil {
		return nil
	}
	c := *r
	c.RetiredRuns = append([]string(nil), r.RetiredRuns...)
	c.UncertainOnce = append([]string(nil), r.UncertainOnce...)
	return &c
}

func initializationRepairTransition(old identityRecord, b *model.Bench) bool {
	w, next := b.Workspace, b.Lease
	r := w.InitializationRepair
	l := old.lease
	if r == nil || l == nil || next == nil || l.Holder != "initialization" || !l.StopRequested || l.Continuation != "" || l.Execution != nil && l.Execution.Phase != "stopped" || next.Holder != "initialization" || next.StopRequested || next.Token == l.Token || next.Continuation == "" || next.Execution != nil || w.Stage != nil || old.archive != nil || w.Archive != nil {
		return false
	}
	if old.phase != "initializing" && old.phase != "prepared" || w.Phase != old.phase && !(old.phase == "prepared" && w.Phase == "initializing" && old.pending == nil) {
		return false
	}
	previous := ""
	if old.repair != nil {
		previous = old.repair.ID
	}
	if r.PreviousID != previous || r.ID == previous || old.project != b.ProjectID || old.bytes != b.QuotaBytes || old.inodes != b.QuotaInodes || !reflect.DeepEqual(old.pending, w.Pending) || !reflect.DeepEqual(old.checkpoint, w.Checkpoint) || !slices.Equal(old.unmerged, w.Unmerged) {
		return false
	}
	if old.pending != nil && !slices.Equal(old.preparation, w.Preparation) {
		return false
	}
	proofs := map[string]bool{}
	for _, p := range w.Preparation {
		if !slices.Contains(old.preparation, p) && !(old.stage != nil && l.Execution != nil && l.Execution.Phase == "stopped" && p.Name == old.stage.Name && p.Digest == old.stage.Digest && p.RunID == l.Execution.ID) {
			return false
		}
		if p.RunID != "" {
			proofs[p.RunID] = true
		}
	}
	if len(next.RetainedRuns) != len(proofs) {
		return false
	}
	if !slices.IsSorted(next.RetainedRuns) {
		return false
	}
	for _, id := range next.RetainedRuns {
		if !proofs[id] {
			return false
		}
	}
	oldRuns := map[string]bool{}
	retired := map[string]bool{}
	for _, id := range r.RetiredRuns {
		retired[id] = true
	}
	for _, id := range l.RetainedRuns {
		oldRuns[id] = true
	}
	if l.Execution != nil {
		oldRuns[l.Execution.ID] = true
	}
	if old.repair != nil {
		for _, id := range old.repair.RetiredRuns {
			oldRuns[id] = true
			if !retired[id] {
				return false
			}
		}
	}
	if old.repair != nil {
		for _, name := range old.repair.UncertainOnce {
			if !slices.Contains(r.UncertainOnce, name) {
				return false
			}
		}
	}
	nextRuns := map[string]bool{}
	for _, id := range append(append([]string(nil), r.RetiredRuns...), next.RetainedRuns...) {
		if nextRuns[id] || !oldRuns[id] {
			return false
		}
		nextRuns[id] = true
	}
	return len(oldRuns) == len(nextRuns)
}

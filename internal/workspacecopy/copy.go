// Package workspacecopy owns private deep workspace record copies.
package workspacecopy

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"slices"
)

func Copy(b *model.Bench) *model.Bench {
	if b == nil {
		return nil
	}
	c := *b
	if b.Workspace != nil {
		w := *b.Workspace
		w.Members = append([]model.WorkspaceMember(nil), w.Members...)
		if w.Transfer != nil {
			t := *w.Transfer
			if t.CrossHost != nil {
				h := *t.CrossHost
				t.CrossHost = &h
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
				t.Recovery = &r
			}
			w.Transfer = &t
		}
		if w.TransferredFrom != nil {
			t := *w.TransferredFrom
			if t.CrossHost != nil {
				h := *t.CrossHost
				t.CrossHost = &h
			}
			w.TransferredFrom = &t
		}
		if w.InitializationRepair != nil {
			r := *w.InitializationRepair
			r.RetiredRuns = append([]string(nil), r.RetiredRuns...)
			r.UncertainOnce = append([]string(nil), r.UncertainOnce...)
			w.InitializationRepair = &r
		}
		w.Unmerged = append([]model.WorkspaceMergeEvidence(nil), w.Unmerged...)
		if w.Archive != nil {
			archive := *w.Archive
			archive.Merge = append([]model.WorkspaceMergeEvidence(nil), archive.Merge...)
			w.Archive = &archive
		}
		if w.RecoveredFrom != nil {
			origin := *w.RecoveredFrom
			w.RecoveredFrom = &origin
		}
		w.Preparation = append([]model.WorkspacePreparation(nil), w.Preparation...)
		cloneCheckpoint := func(cp *model.WorkspaceCheckpoint) *model.WorkspaceCheckpoint {
			if cp == nil {
				return nil
			}
			copy := *cp
			copy.Members = append([]model.WorkspaceCommit(nil), cp.Members...)
			return &copy
		}
		w.Pending, w.Checkpoint = cloneCheckpoint(w.Pending), cloneCheckpoint(w.Checkpoint)
		if w.Stage != nil {
			stage := *w.Stage
			w.Stage = &stage
		}
		c.Workspace = &w
	}
	if b.Lease != nil {
		l := *b.Lease
		l.RetainedRuns = append([]string(nil), l.RetainedRuns...)
		if l.Execution != nil {
			e := *l.Execution
			l.Execution = &e
		}
		c.Lease = &l
	}
	return &c
}

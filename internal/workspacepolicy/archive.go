package workspacepolicy

import (
	"errors"
	"strings"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func archiveTime(s string) (time.Time, bool) {
	v, e := time.Parse(time.RFC3339Nano, s)
	return v, e == nil && !v.IsZero()
}
func archiveEvidence(b *model.Bench, list []model.WorkspaceMergeEvidence, merged bool) error {
	invalid := errors.New("invalid aggregate archive observation")
	if len(list) == 0 {
		return nil
	}
	w := b.Workspace
	if w.Checkpoint == nil || len(w.Checkpoint.Members) != len(w.Members) || merged && len(list) != len(w.Members) {
		return invalid
	}
	changed := false
	for i, p := range list {
		if i > 0 && list[i-1].Name >= p.Name {
			return invalid
		}
		index := -1
		for j, m := range w.Members {
			if p.Name == m.Name {
				index = j
				break
			}
		}
		if index < 0 {
			return invalid
		}
		m := w.Members[index]
		if w.Checkpoint.Members[index].Name != m.Name {
			return invalid
		}
		e := p.Evidence
		current := w.Checkpoint.Members[index].Commit
		changed = changed || current != m.Source
		if e.Checkpoint != current || !digestPattern.MatchString(e.ConfigDigest) || !model.ValidBranchRef(e.TargetBranch) || e.TargetBranch == "refs/heads/"+m.Branch || !model.ValidCommit(e.TargetCommit) || len(e.TargetCommit) != len(current) || strings.ToLower(e.TargetCommit) != e.TargetCommit {
			return invalid
		}
		observed, ok := archiveTime(e.ObservedAt)
		if !ok {
			return invalid
		}
		if !merged && current == m.Source {
			return invalid
		}
		if merged {
			archived, ok := archiveTime(w.Archive.ArchivedAt)
			if !ok || observed.After(archived) {
				return invalid
			}
			if current != m.Source {
				var prior *model.ArchiveMergeEvidence
				for _, u := range w.Unmerged {
					if u.Name == p.Name {
						copy := u.Evidence
						prior = &copy
						break
					}
				}
				if prior == nil || prior.Checkpoint != e.Checkpoint || prior.ConfigDigest != e.ConfigDigest || prior.TargetBranch != e.TargetBranch {
					return invalid
				}
				at, ok := archiveTime(prior.ObservedAt)
				if !ok || at.After(observed) {
					return invalid
				}
			}
		}
	}
	if merged && !changed {
		return invalid
	}
	return nil
}
func validateArchive(b *model.Bench) error {
	invalid := errors.New("invalid aggregate archive lifecycle")
	w := b.Workspace
	a := w.Archive
	if len(w.Unmerged) > 0 && w.Phase != "ready" && w.Phase != "archived" && w.Phase != "reaping" && w.Phase != "reaped" && !Reclaim(w.Phase) {
		return invalid
	}
	if err := archiveEvidence(b, w.Unmerged, false); err != nil {
		return err
	}
	archived := w.Phase == "archived" || w.Phase == "reaping" || w.Phase == "reaped"
	if !archived {
		if a != nil {
			return invalid
		}
		return nil
	}
	if a == nil || b.Lease != nil || w.Pending != nil || w.Stage != nil || w.Checkpoint == nil || !digestPattern.MatchString(a.FreezeDigest) {
		return invalid
	}
	at, ok := archiveTime(a.ArchivedAt)
	if !ok {
		return invalid
	}
	deadline, ok := archiveTime(a.ReapAfter)
	if !ok || deadline.Before(at) || deadline.After(at.Add(365*24*time.Hour)) {
		return invalid
	}
	var frozen time.Time
	if a.FrozenAt != "" {
		var ok bool
		frozen, ok = archiveTime(a.FrozenAt)
		if !ok || frozen.Before(at) {
			return invalid
		}
	}
	if a.ReapingDigest != "" && (!digestPattern.MatchString(a.ReapingDigest) || frozen.IsZero()) {
		return invalid
	}
	switch w.Phase {
	case "archived":
		if a.ReapingDigest != "" || a.ReapedAt != "" {
			return invalid
		}
	case "reaping":
		if a.ReapingDigest == "" || a.ReapedAt != "" {
			return invalid
		}
	case "reaped":
		done, ok := archiveTime(a.ReapedAt)
		if a.ReapingDigest == "" || !ok || done.Before(deadline) || done.Before(frozen) {
			return invalid
		}
	}
	return archiveEvidence(b, a.Merge, true)
}

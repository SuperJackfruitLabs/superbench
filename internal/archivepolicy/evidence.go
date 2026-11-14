package archivepolicy

import (
	"encoding/hex"
	"errors"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func validateMerge(b *model.Bench) error {
	hash := func(s string, lengths ...int) bool {
		raw, err := hex.DecodeString(s)
		for _, n := range lengths {
			if err == nil && len(raw) == n {
				return true
			}
		}
		return false
	}
	for _, e := range []*model.ArchiveMergeEvidence{b.ArchiveUnmerged, b.ArchiveMerge} {
		if e == nil {
			continue
		}
		observed, err := time.Parse(time.RFC3339Nano, e.ObservedAt)
		if !model.ValidCommit(b.CreatedFrom) || e.Checkpoint == b.CreatedFrom || e.Checkpoint != b.Checkpoint ||
			!hash(e.Checkpoint, 20, 32) || !hash(e.TargetCommit, 20, 32) || !hash(e.ConfigDigest, 32) ||
			!model.ValidBranchRef(e.TargetBranch) || e.TargetBranch == "refs/heads/"+b.Branch || err != nil || observed.IsZero() {
			return errors.New("invalid automatic archive observation")
		}
	}
	if e := b.ArchiveMerge; e != nil {
		u := b.ArchiveUnmerged
		observed, _ := time.Parse(time.RFC3339Nano, e.ObservedAt)
		archived, _ := time.Parse(time.RFC3339Nano, b.ArchivedAt)
		if b.ArchivedAt == "" || b.ArchiveFreezeDigest == "" || u == nil ||
			u.Checkpoint != e.Checkpoint || u.ConfigDigest != e.ConfigDigest || u.TargetBranch != e.TargetBranch || observed.After(archived) {
			return errors.New("invalid automatic archive merge evidence")
		}
		unmerged, _ := time.Parse(time.RFC3339Nano, u.ObservedAt)
		if unmerged.After(observed) {
			return errors.New("automatic archive observation order changed")
		}
	}
	return nil
}

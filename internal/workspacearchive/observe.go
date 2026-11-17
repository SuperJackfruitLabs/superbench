package workspacearchive

import (
	"context"
	"errors"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

var ErrIneligible = errors.New("workspace has no confirmed whole-vector branch-merged archive trigger")

// NoteCheckpoint optionally records changed members before integration. Its
// caller holds the host lock and commits this evidence with the completed seal.
// Failed observations never reject an otherwise valid checkpoint.
func NoteCheckpoint(ctx context.Context, mirrors repository.Mirrors, b *model.Bench) {
	if fresh, err := freshtree.Selected(nil, mirrors.State, b); err != nil || fresh {
		return // merge observation refreshes the checkout as the daemon
	}
	w := b.Workspace
	prior := w.Unmerged
	w.Unmerged = nil
	for i, member := range w.Members {
		commit := w.Checkpoint.Members[i].Commit
		if commit == member.Source {
			continue
		}
		retained := false
		for _, p := range prior {
			if p.Name == member.Name && p.Evidence.Checkpoint == commit {
				w.Unmerged = append(w.Unmerged, p)
				retained = true
				break
			}
		}
		if retained {
			continue
		}
		config, err := setup.At(ctx, member.Mirror, commit)
		if err != nil || config == nil || config.ArchiveWhen() != "branch-merged" {
			continue
		}
		observation, err := mirrors.ObserveMerge(ctx, member.Repository, repository.Checkout{Path: member.Path, Branch: member.Branch, Mirror: member.Mirror}, commit)
		if err != nil || observation.Branch == "" || observation.Merged {
			continue
		}
		w.Unmerged = append(w.Unmerged, model.WorkspaceMergeEvidence{Name: member.Name, Evidence: model.ArchiveMergeEvidence{
			Checkpoint: commit, ConfigDigest: config.Digest, TargetBranch: observation.Branch,
			TargetCommit: observation.Commit, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}})
	}
}

// CheckMerged gates only a new automatic archive intent. Every member must opt
// in at its sealed configuration and be integrated; changed members also need a
// prior observation of the same checkpoint before integration into that target.
func CheckMerged(ctx context.Context, mirrors repository.Mirrors, b *model.Bench) error {
	w := b.Workspace
	if w == nil || w.Checkpoint == nil || w.Pending != nil || w.Archive == nil || b.Lease != nil {
		return ErrIneligible
	}
	var merged []model.WorkspaceMergeEvidence
	changed := false
	for i, member := range w.Members {
		if err := ctx.Err(); err != nil {
			return err
		}
		commit := w.Checkpoint.Members[i].Commit
		config, err := setup.At(ctx, member.Mirror, commit)
		if err != nil {
			return err
		}
		if config == nil || config.ArchiveWhen() != "branch-merged" {
			return ErrIneligible
		}
		actual, err := setup.Read(member.Path)
		if err != nil {
			return err
		}
		if actual == nil || actual.Digest != config.Digest {
			return errors.New("workspace member archive policy differs from its checkpoint")
		}
		var prior *model.ArchiveMergeEvidence
		if commit != member.Source {
			changed = true
			for _, p := range w.Unmerged {
				if p.Name == member.Name && p.Evidence.Checkpoint == commit && p.Evidence.ConfigDigest == config.Digest {
					evidence := p.Evidence
					prior = &evidence
				}
			}
			if prior == nil {
				return ErrIneligible
			}
		}
		observation, err := mirrors.Merged(ctx, member.Repository, repository.Checkout{Path: member.Path, Branch: member.Branch, Mirror: member.Mirror}, commit)
		if err != nil {
			return err
		}
		if observation.Commit == "" || prior != nil && observation.Branch != prior.TargetBranch {
			return ErrIneligible
		}
		merged = append(merged, model.WorkspaceMergeEvidence{Name: member.Name, Evidence: model.ArchiveMergeEvidence{
			Checkpoint: commit, ConfigDigest: config.Digest, TargetBranch: observation.Branch,
			TargetCommit: observation.Commit, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}})
	}
	if !changed {
		return ErrIneligible
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w.Archive.Merge = merged
	return nil
}

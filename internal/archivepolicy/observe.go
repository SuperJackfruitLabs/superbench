package archivepolicy

import (
	"context"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

// NoteCheckpoint records a published checkpoint observed before its integration.
// The caller holds the durable bench transaction and shared mirror lock. This
// optional policy observation never refuses an otherwise valid checkpoint.
func NoteCheckpoint(ctx context.Context, mirrors repository.Mirrors, b *model.Bench) {
	prior := b.ArchiveUnmerged
	b.ArchiveUnmerged = nil
	if b.CreatedFrom == "" || b.Checkpoint == "" || b.Checkpoint == b.CreatedFrom {
		return
	}
	// A repeated checkpoint cannot change its immutable policy bytes. Preserve
	// validated prior evidence even if this commit has since been integrated or
	// the optional observation service is unavailable. Archive rechecks policy
	// and the observed default branch before consuming this proof.
	if prior != nil && prior.Checkpoint == b.Checkpoint && prior.ConfigDigest != "" {
		b.ArchiveUnmerged = prior
		return
	}
	timeout := mirrors.Timeout
	if timeout == 0 {
		timeout = repository.DefaultTimeout
	}
	if timeout < 0 || timeout > time.Hour {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	config, err := setup.At(ctx, b.Mirror, b.Checkpoint)
	if err != nil || config == nil || config.ArchiveWhen() != "branch-merged" {
		return
	}
	observation, err := mirrors.ObserveMerge(ctx, b.Repo, repository.Checkout{Path: b.Path, Branch: b.Branch, Mirror: b.Mirror}, b.Checkpoint)
	if err != nil || observation.Branch == "" || observation.Merged {
		return
	}
	b.ArchiveUnmerged = &model.ArchiveMergeEvidence{
		Checkpoint: b.Checkpoint, ConfigDigest: config.Digest, TargetBranch: observation.Branch,
		TargetCommit: observation.Commit, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

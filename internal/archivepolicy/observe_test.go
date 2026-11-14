package archivepolicy

import (
	"context"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func TestRepeatedCheckpointPreservesPriorUnmergedProofWithoutObservation(t *testing.T) {
	proof := &model.ArchiveMergeEvidence{
		Checkpoint: strings.Repeat("a", 40), ConfigDigest: strings.Repeat("b", 64),
		TargetBranch: "refs/heads/main", TargetCommit: strings.Repeat("c", 40),
		ObservedAt: "2030-01-01T00:00:00Z",
	}
	b := model.Bench{CreatedFrom: strings.Repeat("d", 40), Checkpoint: proof.Checkpoint, ArchiveUnmerged: proof}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The mirror is intentionally unusable and the context canceled: retaining
	// this already validated immutable proof must not depend on another Git read.
	NoteCheckpoint(ctx, repository.Mirrors{}, &b)
	if b.ArchiveUnmerged != proof {
		t.Fatal("same checkpoint lost prior observation")
	}
}

func TestCheckpointChangeClearsPriorUnmergedProof(t *testing.T) {
	source, old, next := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	for _, tc := range []struct{ name, created, checkpoint, digest string }{
		{"new checkpoint", source, next, "digest"},
		{"initial source", source, source, "digest"},
		{"missing provenance", "", old, "digest"},
		{"empty checkpoint", source, "", "digest"},
		{"missing proof digest", source, old, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := model.Bench{CreatedFrom: tc.created, Checkpoint: tc.checkpoint, ArchiveUnmerged: &model.ArchiveMergeEvidence{Checkpoint: old, ConfigDigest: tc.digest}}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			NoteCheckpoint(ctx, repository.Mirrors{}, &b)
			if b.ArchiveUnmerged != nil {
				t.Fatal("stale observation survived", b.ArchiveUnmerged)
			}
		})
	}
}

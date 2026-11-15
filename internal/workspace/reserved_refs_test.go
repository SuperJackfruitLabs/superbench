package workspace

import (
	"strings"
	"testing"
)

func TestTaskBranchesCannotOccupyCheckpointMetadataNamespace(t *testing.T) {
	for _, branch := range []string{"superbench", "superbench/checkpoints", "superbench/checkpoints/other-workspace"} {
		t.Run(branch, func(t *testing.T) {
			members := []Repository{{Name: "a", Repository: "/fixture", Branch: branch}}
			if err := ValidateStage("create", Spec{Key: "card", CheckpointRepository: "a", Repositories: members}); err == nil {
				t.Fatal("creation accepted a task ref in the metadata namespace")
			}
			if err := ValidateRecovery("recover", RecoverySpec{Key: "card", Repositories: members, Source: RecoverySource{Repository: "/fixture", BenchID: strings.Repeat("a", 32), Commit: strings.Repeat("b", 40), ManifestSHA256: strings.Repeat("c", 64)}}); err == nil {
				t.Fatal("recovery accepted a task ref in the metadata namespace")
			}
		})
	}
	// Reading a committed source never takes ownership of its branch namespace.
	if err := ValidateStage("create", Spec{Key: "card", CheckpointRepository: "a", Repositories: []Repository{{Name: "a", Repository: "/fixture", Base: "superbench/checkpoints/source"}}}); err != nil {
		t.Fatal("metadata source became unavailable for read-only selection", err)
	}
}

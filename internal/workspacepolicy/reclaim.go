package workspacepolicy

import (
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// Reclaim reports the two terminal phases of a released Fresh workspace whose
// tree the fixed helper deletes: reclaiming, retained before any effect and
// never returned to ready, and reclaimed, once its quota project is retired.
func Reclaim(phase string) bool { return phase == "reclaiming" || phase == "reclaimed" }

// validateReclaim requires a reclaim phase to keep the released, allocated,
// checkpointed record it left ready as. A pending or staged generation, an
// archive, a transfer or a repair is already refused for a record without a
// writer by their own rules.
func validateReclaim(b *model.Bench) error {
	if Reclaim(b.Workspace.Phase) && (b.Lease != nil || b.Workspace.Checkpoint == nil || b.ProjectID == 0) {
		return errors.New("invalid aggregate reclaim")
	}
	return nil
}

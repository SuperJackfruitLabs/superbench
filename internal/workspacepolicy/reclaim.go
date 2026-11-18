package workspacepolicy

import (
	"errors"
	"time"

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

// validIdleSince accepts no idle timestamp (a record from before it existed)
// or one in the RFC 3339 UTC form the state update and the sweep write.
func validIdleSince(s string) bool {
	if s == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	return err == nil && t.Location() == time.UTC && t.Format(time.RFC3339Nano) == s
}

// IdleSince is when a released workspace's idle grace began, and whether the
// record holds that time. A record without one is idle from first sight.
func IdleSince(b *model.Bench) (time.Time, bool) {
	if b == nil || b.Workspace == nil || b.Workspace.IdleSince == "" || !validIdleSince(b.Workspace.IdleSince) {
		return time.Time{}, false
	}
	t, _ := time.Parse(time.RFC3339Nano, b.Workspace.IdleSince)
	return t, true
}

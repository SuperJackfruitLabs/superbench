package freshrecovery

import (
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cleanworker"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshliveness"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

// ErrReclaimBusy and ErrReclaimPayloadActive are root's own liveness refusals.
var (
	ErrReclaimBusy          = freshliveness.ErrBusy
	ErrReclaimPayloadActive = freshliveness.ErrPayloadActive
)

// errLeafGone refuses a completed bench whose leaf is already missing: that is
// not a reclaim's to finish, and Fresh recovery inspects it instead.
var errLeafGone = errors.New("fresh reclaim refused: a completed bench's leaf is missing; inspect it with Fresh recovery")

// workerAccount is the only account each worker mode runs as: the daemon,
// which owns a leaked bootstrap tree, to observe or delete it, and the payload
// account, which owns a reclaimed bench's tree, to purge it. A purge so cannot
// remove anything the payload could not remove itself.
func workerAccount(p payloadpolicy.Policy, mode string) (uint32, uint32, error) {
	switch mode {
	case cleanworker.Observe, cleanworker.Delete:
		if p.DaemonUID != 0 && p.DaemonGID != 0 {
			return p.DaemonUID, p.DaemonGID, nil
		}
	case cleanworker.Purge:
		if p.PayloadUID != 0 && p.PayloadGID != 0 && p.PayloadUID != p.DaemonUID && p.PayloadGID != p.DaemonGID {
			return p.PayloadUID, p.PayloadGID, nil
		}
	}
	return 0, 0, ErrRecovery
}

// watched reports the worker modes root watches for progress: a purge runs as
// the payload account, which can stop it.
func watched(mode string) bool { return mode == cleanworker.Purge }

// reclaimSteps are one reclaim's effects. Each is idempotent for the retained
// intent, so a retry after a crash or a lost reply runs the same sequence.
type reclaimSteps struct {
	// phase is the bootstrap intent's phase when the step began.
	phase func() (string, error)
	// leaf reports the bench's own leaf present: the recorded node on the
	// pinned data device, payload-owned, in the assigned project. Anything else
	// at that name refuses.
	leaf func() (bool, error)
	// fenced requires the bootstrap's Git controls fenced.
	fenced func() error
	// idle refuses (ErrReclaimBusy) when root's own check, never the daemon's
	// record, sees the bench in use. It is best-effort and point in time; the
	// empty-leaf removal and the zero-usage retirement are the guarantees. It
	// runs before retention and again before the purge.
	idle func() error
	// retain moves the intent completed -> reclaiming, before any effect.
	retain func() error
	// purge empties the leaf as the payload account; remove is root's removal
	// of the then empty leaf; retire proves usage zero and saves the tombstone
	// (and writes nothing on an exact retry); finish moves the intent to
	// reclaimed.
	purge, remove, retire, finish func() error
}

// run applies the only order: idle, retain, idle, purge, remove, retire,
// finish. It reports whether the reclaiming intent was retained, after which
// any error may follow effects and the caller must say so.
func (r reclaimSteps) run() (retained bool, err error) {
	phase, err := r.phase()
	if err != nil {
		return false, err
	}
	switch phase {
	case "completed":
		present, err := r.leaf()
		if err != nil {
			return false, err
		}
		if !present {
			return false, errLeafGone
		}
		if err = r.fenced(); err != nil {
			return false, err
		}
		if err = r.idle(); err != nil {
			return false, err
		}
		// A failed write can follow its rename: report it as retained.
		if err = r.retain(); err != nil {
			return true, err
		}
	case "reclaiming", "reclaimed":
	default:
		return false, ErrRecovery
	}
	present, err := r.leaf()
	if err != nil {
		return true, err
	}
	if present {
		if phase == "reclaimed" {
			return true, ErrRecovery
		}
		if err = r.idle(); err != nil {
			return true, err
		}
		if err = r.purge(); err != nil {
			return true, err
		}
		if err = r.remove(); err != nil {
			return true, err
		}
	}
	if err = r.retire(); err != nil {
		return true, err
	}
	if phase == "reclaimed" {
		return true, nil
	}
	return true, r.finish()
}

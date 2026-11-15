package operationreceipts

import "database/sql"

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"

type CancellationInventory struct {
	State   string
	Intent  operationcontrol.Intent
	Helpers []operationcontrol.Helper
}

// CancellationInventory snapshots the original exact operation and its closed
// helper vector under the same independent admission mutex. No waits occur here.
func (r *receipts) CancellationInventory(t operationcontrol.Target) (CancellationInventory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out CancellationInventory
	var state, barrier, digest string
	if err := r.db.QueryRow(`SELECT state,cancel_state,digest FROM receipts WHERE owner=? AND id=?`, t.UID, t.RequestID).Scan(&state, &barrier, &digest); err != nil {
		if err == sql.ErrNoRows {
			if err = r.db.QueryRow(`SELECT digest,state FROM operation_cancels WHERE owner=? AND id=?`, t.UID, t.RequestID).Scan(&digest, &state); err != nil {
				return out, errInDoubt
			}
			if digest != t.Digest {
				return out, errConflict
			}
			if state == "cancelled" {
				out.State = "cancelled"
			} else if state == "cancel_requested" {
				out.State = "not_admitted"
			} else {
				return out, errInDoubt
			}
			return out, nil
		}
		return out, err
	}
	if digest != t.Digest {
		return out, errConflict
	}
	if state == "done" {
		out.State = "already_completed"
		return out, nil
	}
	if barrier == "cancelled" {
		out.State = "cancelled"
		return out, nil
	}
	if barrier != "cancel_requested" {
		return out, errInDoubt
	}
	var err error
	out.Intent, err = r.operationIntent(t)
	if err != nil {
		return out, err
	}
	out.Helpers, err = r.operationHelpers(out.Intent)
	if err != nil {
		return out, err
	}
	out.State = "cancel_requested"
	return out, nil
}

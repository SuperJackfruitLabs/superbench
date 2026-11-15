package operationreceipts

import (
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// NoEffectsSnapshot binds an empty helper inventory to the exact closed target.
// It is not sufficient to confirm cancellation without the independent core
// admission/run/Git absence proof under the host session.
type NoEffectsSnapshot struct {
	Target operationcontrol.Target
	Intent operationcontrol.Intent
	Typed  bool
}

func (r *receipts) NoEffectsInventory(t operationcontrol.Target) (NoEffectsSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.noEffectsInventoryLocked(t)
}
func (r *receipts) noEffectsInventoryLocked(t operationcontrol.Target) (NoEffectsSnapshot, error) {
	out := NoEffectsSnapshot{Target: t}
	if !t.Valid() {
		return out, errConflict
	}
	var digest, state, barrier string
	err := r.db.QueryRow(`SELECT digest,state,cancel_state FROM receipts WHERE owner=? AND id=?`, t.UID, t.RequestID).Scan(&digest, &state, &barrier)
	if err == nil {
		if digest != t.Digest {
			return out, errConflict
		}
		if (state != "running" && state != "interrupted") || (barrier != "cancel_requested" && barrier != "cancelled") {
			return out, errInDoubt
		}
		out.Intent, err = r.operationIntent(t)
		if err != nil {
			return out, err
		}
		helpers, e := r.operationHelpers(out.Intent)
		if e != nil {
			return out, e
		}
		if len(helpers) != 0 {
			return out, errInDoubt
		}
		out.Typed = true
		return out, nil
	}
	if err != sql.ErrNoRows {
		return out, err
	}
	if _, known := r.active[receiptKey(t.UID, t.RequestID)]; known {
		return out, errInDoubt
	}
	if _, err = r.operationIntent(t); err != sql.ErrNoRows {
		if err == nil {
			return out, errInDoubt
		}
		return out, err
	}
	if retained, e := r.retainedHelpers(t.UID, t.RequestID); e != nil || retained {
		if e != nil {
			return out, e
		}
		return out, errInDoubt
	}
	if err = r.db.QueryRow(`SELECT digest,state FROM operation_cancels WHERE owner=? AND id=?`, t.UID, t.RequestID).Scan(&digest, &state); err != nil {
		return out, err
	}
	if digest != t.Digest {
		return out, errConflict
	}
	if state != "cancel_requested" && state != "cancelled" {
		return out, errInDoubt
	}
	return out, nil
}
func (r *receipts) VerifyNoEffects(expected NoEffectsSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	got, err := r.noEffectsInventoryLocked(expected.Target)
	if err != nil {
		return err
	}
	if got != expected {
		return errConflict
	}
	return nil
}

// ConfirmNoEffects is called only after the independent engine absence proof;
// neither a context cancellation nor an empty helper vector alone can call it.
func (r *receipts) ConfirmNoEffects(expected NoEffectsSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	got, err := r.noEffectsInventoryLocked(expected.Target)
	if err != nil {
		return err
	}
	if got != expected {
		return errConflict
	}
	if expected.Typed {
		return r.confirmCancellationLocked(expected.Intent)
	}
	t := expected.Target
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE operation_cancels SET state='cancelled' WHERE owner=? AND id=? AND digest=? AND state IN ('cancel_requested','cancelled')`, t.UID, t.RequestID, t.Digest)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errInDoubt
	}
	var state string
	if err = tx.QueryRow(`SELECT state FROM operation_cancels WHERE owner=? AND id=? AND digest=?`, t.UID, t.RequestID, t.Digest).Scan(&state); err != nil {
		return err
	}
	if state != "cancelled" {
		return errInDoubt
	}
	return tx.Commit()
}

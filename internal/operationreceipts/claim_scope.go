package operationreceipts

import (
	"context"
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// ClaimScope publishes the receipt and its tracked typed authority in one
// transaction before attaching a controller under the same admission mutex.
// Restart can prove a committed pre-engine interruption had no effects; a
// historical receipt never gains a fresh epoch or controller.
func (r *receipts) ClaimScope(parent context.Context, i operationcontrol.Intent) (*Receipt, context.Context, context.CancelFunc, error) {
	if !i.Valid() {
		return nil, nil, nil, errConflict
	}
	if _, exists := operationcontrol.FromContext(parent); exists {
		return nil, nil, nil, errConflict
	}
	if err := parent.Err(); err != nil {
		return nil, nil, nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cached, found, err := r.lookupLocked(i.Target.UID, i.Target.RequestID, i.Target.Digest); found || err != nil {
		return cached, nil, nil, err
	}
	if err := r.claimTypedLocked(i); err != nil {
		return nil, nil, nil, err
	}
	key := receiptKey(i.Target.UID, i.Target.RequestID)
	r.active[key] = true
	ctx, stop, err := r.operationScopeLocked(parent, i, true)
	if err != nil {
		r.active[key] = false
		return nil, nil, nil, err
	}
	return nil, ctx, stop, nil
}

func (r *receipts) claimTypedLocked(i operationcontrol.Intent) error {
	t := i.Target
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = cancelBarrier(tx, t.UID, t.RequestID, t.Digest); err != nil {
		return err
	}
	if err = requireReceiptCapacity(tx); err != nil {
		return err
	}
	var count int64
	if err = tx.QueryRow(`SELECT count(*) FROM operation_intents WHERE core_finalized=0`).Scan(&count); err != nil {
		return err
	}
	if count >= operationcontrol.MaxOperations {
		return errCapacity
	}
	result, err := tx.Exec(`INSERT INTO receipts(owner,id,digest,state) VALUES(?,?,?,'running')`, t.UID, t.RequestID, t.Digest)
	if err = oneInserted(result, err); err != nil {
		return err
	}
	result, err = tx.Exec(`INSERT INTO operation_intents(owner,id,digest,key,operation,epoch,helper_tracking) VALUES(?,?,?,?,?,?,1)`, t.UID, t.RequestID, t.Digest, i.Key, i.Operation, i.Epoch)
	if err = oneInserted(result, err); err != nil {
		return err
	}
	stored, err := loadOperationIntent(tx, t)
	if err != nil {
		return err
	}
	if stored != i {
		return errInDoubt
	}
	var tracked, finalized int
	if err = tx.QueryRow(`SELECT helper_tracking,core_finalized FROM operation_intents WHERE owner=? AND id=?`, t.UID, t.RequestID).Scan(&tracked, &finalized); err != nil {
		return err
	}
	if tracked != 1 || finalized != 0 {
		return errInDoubt
	}
	var digest, state, barrier string
	var body []byte
	var status sql.NullInt64
	if err = tx.QueryRow(`SELECT digest,state,cancel_state,body,status FROM receipts WHERE owner=? AND id=?`, t.UID, t.RequestID).Scan(&digest, &state, &barrier, &body, &status); err != nil {
		return err
	}
	if digest != t.Digest || state != "running" || barrier != "" || body != nil || status.Valid {
		return errInDoubt
	}
	return tx.Commit()
}
func oneInserted(result sql.Result, err error) error {
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
	return nil
}

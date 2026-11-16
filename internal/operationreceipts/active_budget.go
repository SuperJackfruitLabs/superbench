package operationreceipts

import (
	"database/sql"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

const activeReceiptBytes int64 = 64 << 20
const receiptReservation int64 = 64 << 10

// Every unresolved identity reserves a whole bounded response, including work
// interrupted by restart and typed responses awaiting independent core cutoff.
// Historical rows are never removed to recover this admission capacity.
func requireReceiptCapacity(tx *sql.Tx) error {
	var count int64
	err := tx.QueryRow(`SELECT count(*) FROM (
 SELECT owner,id FROM operation_intents WHERE core_finalized=0
 UNION ALL
 SELECT r.owner,r.id FROM receipts r WHERE r.state<>'done' AND r.cancel_state<>'cancelled'
 AND NOT EXISTS(SELECT 1 FROM operation_intents i WHERE i.owner=r.owner AND i.id=r.id)
 )`).Scan(&count)
	if err != nil {
		return err
	}
	if count < 0 || count >= activeReceiptBytes/receiptReservation {
		return errCapacity
	}
	return nil
}

// These indexes are derived accelerators, not new terminal authority. Keeping
// schema6 and every original row means old binaries retain their stricter cap;
// they never mistake archived-away identities for fresh work.
func activeIndexes(db *sql.DB) error {
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS receipt_unresolved ON receipts(owner,id) WHERE state<>'done' AND cancel_state<>'cancelled';
 CREATE INDEX IF NOT EXISTS intent_unresolved ON operation_intents(owner,id) WHERE core_finalized=0;
 CREATE INDEX IF NOT EXISTS cancel_unresolved ON operation_cancels(owner,id) WHERE state='cancel_requested'`)
	return err
}

// An already-bound controller rechecks the same durable intent at each effect.
// This is not used for the initial untyped-to-typed admission transition.
func (r *receipts) boundOperationAdmission(i operationcontrol.Intent) error {
	if err := r.operationAdmission(i); err != nil {
		return err
	}
	stored, err := r.operationIntent(i.Target)
	if err != nil {
		return errInDoubt
	}
	if stored != i {
		return errConflict
	}
	var finalized int
	if err = r.db.QueryRow(`SELECT core_finalized FROM operation_intents WHERE owner=? AND id=?`, i.Target.UID, i.Target.RequestID).Scan(&finalized); err != nil {
		return err
	}
	if finalized != 0 {
		return errInDoubt
	}
	return nil
}

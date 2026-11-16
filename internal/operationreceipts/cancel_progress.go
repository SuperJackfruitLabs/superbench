package operationreceipts

import (
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// This bit records completion of independent core reconciliation; it cannot
// authorize a fence or stand in for the original durable response cutoff.
func (r *receipts) markCoreFinalized(i operationcontrol.Intent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.finalizationCutoffLocked(i); err != nil {
		return err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = saveCoreFinalized(tx, i); err != nil {
		return err
	}
	return tx.Commit()
}

func saveCoreFinalized(tx *sql.Tx, i operationcontrol.Intent) error {
	result, err := tx.Exec(`UPDATE operation_intents SET core_finalized=1 WHERE owner=? AND id=? AND digest=? AND key=? AND operation=? AND epoch=? AND core_finalized IN (0,1)`, i.Target.UID, i.Target.RequestID, i.Target.Digest, i.Key, i.Operation, i.Epoch)
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
	var saved int
	if err = tx.QueryRow(`SELECT core_finalized FROM operation_intents WHERE owner=? AND id=?`, i.Target.UID, i.Target.RequestID).Scan(&saved); err != nil {
		return err
	}
	if saved != 1 {
		return errInDoubt
	}
	return nil
}

// At most one bounded batch is materialized. Successful core reconciliation
// advances the durable bit, so completed old rows cannot monopolize every batch.
func (r *receipts) completedOperations() ([]operationcontrol.Intent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int
	if err := r.db.QueryRow(`SELECT count(*) FROM operation_intents WHERE core_finalized=0`).Scan(&count); err != nil {
		return nil, err
	}
	if count > operationcontrol.MaxOperations {
		return nil, errInDoubt
	}
	rows, err := r.db.Query(`SELECT i.owner,i.id,i.digest,i.key,i.operation,i.epoch FROM operation_intents i JOIN receipts r ON r.owner=i.owner AND r.id=i.id AND r.digest=i.digest WHERE i.core_finalized=0 AND r.state='done' AND r.cancel_state='' ORDER BY i.owner,i.id LIMIT 64`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []operationcontrol.Intent
	for rows.Next() {
		var i operationcontrol.Intent
		if err = rows.Scan(&i.Target.UID, &i.Target.RequestID, &i.Target.Digest, &i.Key, &i.Operation, &i.Epoch); err != nil {
			return nil, err
		}
		if !i.Valid() {
			return nil, errInDoubt
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

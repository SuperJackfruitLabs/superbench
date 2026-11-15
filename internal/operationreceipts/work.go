package operationreceipts

import (
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// WorkCursor advances through all retained typed intents, including unavailable
// or already completed entries. It is not an operation capability.
type WorkCursor struct {
	UID       uint32
	RequestID string
}
type ReconciliationWork struct {
	Intent    operationcontrol.Intent
	Completed bool
}

// ReconciliationBatch returns at most 64 items and a continuation over the
// durable inventory, so a waiting helper cannot permanently monopolize its
// first page. Empty continuation means the end of this pass. Reading a work item
// is never proof that cancellation completed or permission to replay its body.
func (r *receipts) ReconciliationBatch(after WorkCursor) ([]ReconciliationWork, WorkCursor, error) {
	if after.RequestID == "" && after.UID != 0 || after.RequestID != "" && !cancelID.MatchString(after.RequestID) {
		return nil, WorkCursor{}, errConflict
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int
	if err := r.db.QueryRow(`SELECT count(*) FROM operation_intents`).Scan(&count); err != nil {
		return nil, WorkCursor{}, err
	}
	if count > 10000 {
		return nil, WorkCursor{}, errInDoubt
	}
	rows, err := r.db.Query(`SELECT i.owner,i.id,i.digest,i.key,i.operation,i.epoch,i.core_finalized,r.digest,r.state,r.cancel_state FROM operation_intents i LEFT JOIN receipts r ON r.owner=i.owner AND r.id=i.id WHERE i.owner>? OR (i.owner=? AND i.id>?) ORDER BY i.owner,i.id LIMIT 64`, after.UID, after.UID, after.RequestID)
	if err != nil {
		return nil, WorkCursor{}, err
	}
	defer rows.Close()
	var out []ReconciliationWork
	var next WorkCursor
	var scanned int
	for rows.Next() {
		var w ReconciliationWork
		var finalized int
		var digest, state, barrier sql.NullString
		if err = rows.Scan(&w.Intent.Target.UID, &w.Intent.Target.RequestID, &w.Intent.Target.Digest, &w.Intent.Key, &w.Intent.Operation, &w.Intent.Epoch, &finalized, &digest, &state, &barrier); err != nil {
			return nil, WorkCursor{}, err
		}
		if !w.Intent.Valid() || finalized < 0 || finalized > 1 {
			return nil, WorkCursor{}, errInDoubt
		}
		scanned++
		next = WorkCursor{w.Intent.Target.UID, w.Intent.Target.RequestID}
		if !digest.Valid || digest.String != w.Intent.Target.Digest || !state.Valid || !barrier.Valid {
			return nil, WorkCursor{}, errInDoubt
		}
		if finalized == 1 {
			continue
		}
		w.Completed = state.String == "done" && barrier.String == ""
		if w.Completed || barrier.String == "cancel_requested" {
			out = append(out, w)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, WorkCursor{}, err
	}
	if scanned < 64 {
		next = WorkCursor{}
	}
	return out, next, nil
}

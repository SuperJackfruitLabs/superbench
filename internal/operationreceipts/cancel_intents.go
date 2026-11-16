package operationreceipts

import (
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// A typed, durable v2 intent precedes every engine effect. Generic v1 and
// historical interrupted receipts do not gain operation-epoch authority.
func (r *receipts) operationIntent(t operationcontrol.Target) (operationcontrol.Intent, error) {
	return loadOperationIntent(r.db, t)
}

func loadOperationIntent(q cancelReader, t operationcontrol.Target) (operationcontrol.Intent, error) {
	var i operationcontrol.Intent
	i.Target.UID = t.UID
	i.Target.RequestID = t.RequestID
	err := q.QueryRow(`SELECT digest,key,operation,epoch FROM operation_intents WHERE owner=? AND id=?`, t.UID, t.RequestID).Scan(&i.Target.Digest, &i.Key, &i.Operation, &i.Epoch)
	if err != nil {
		return i, err
	}
	if !i.Valid() {
		return i, errInDoubt
	}
	if i.Target != t {
		return i, errConflict
	}
	return i, nil
}

// Caller holds receipts.mu and has independently validated active admission.
func (r *receipts) saveOperationIntent(i operationcontrol.Intent) error {
	if _, err := r.operationIntent(i.Target); err != sql.ErrNoRows {
		if err == nil {
			return errInDoubt
		}
		return err
	}
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM operation_intents WHERE core_finalized=0`).Scan(&n); err != nil {
		return err
	}
	if n >= 10000 {
		return errCapacity
	}
	result, err := r.db.Exec(`INSERT INTO operation_intents(owner,id,digest,key,operation,epoch,helper_tracking) VALUES(?,?,?,?,?,?,1)`, i.Target.UID, i.Target.RequestID, i.Target.Digest, i.Key, i.Operation, i.Epoch)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errInDoubt
	}
	return r.requireHelperTracking(i)
}

// requestCancelV2 is the future transport boundary, still unwired. A known v1
// or unbound historical receipt remains unchanged; missing typed authority is
// never reinterpreted as proof that its work had no effects.
func (r *receipts) requestCancelV2(uid uint32, id, digest string) (string, error) {
	if !cancelID.MatchString(id) || !cancelDigest.MatchString(digest) {
		return "", errConflict
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, typedErr := r.operationIntent(operationcontrol.Target{UID: uid, RequestID: id, Digest: digest})
	if typedErr != nil && typedErr != sql.ErrNoRows {
		return "", typedErr
	}
	var prior string
	err := r.db.QueryRow(`SELECT digest FROM receipts WHERE owner=? AND id=?`, uid, id).Scan(&prior)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if err == nil {
		if prior != digest {
			return "", errConflict
		}
		if typedErr != nil {
			return "", errInDoubt
		}
	} else if typedErr == nil {
		return "", errInDoubt
	}
	if typedErr != nil {
		if retained, e := r.retainedHelpers(uid, id); e != nil || retained {
			if e != nil {
				return "", e
			}
			return "", errInDoubt
		}
	}
	return r.requestCancelLocked(uid, id, digest)
}

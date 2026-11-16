package operationreceipts

import (
	"bytes"
	"database/sql"
	"encoding/json"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func helperRecord(raw string, i operationcontrol.Intent) (operationcontrol.Helper, error) {
	var h operationcontrol.Helper
	if len(raw) > operationcontrol.MaxHelperIntent || json.Unmarshal([]byte(raw), &h) != nil || !h.Valid(i) {
		return h, errInDoubt
	}
	canonical, err := json.Marshal(h)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		return h, errInDoubt
	}
	return h, nil
}

// recordOperationHelper owns only the independent receipt mutex/database. It
// never acquires a host lock or waits for a subprocess while admission is held.
func (r *receipts) recordOperationHelper(i operationcontrol.Intent, h operationcontrol.Helper) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !h.Valid(i) {
		return errConflict
	}
	if err := r.boundOperationAdmission(i); err != nil {
		return err
	}
	stored, err := r.operationIntent(i.Target)
	if err != nil {
		return err
	}
	if stored != i {
		return errConflict
	}
	if err = r.requireHelperTracking(i); err != nil {
		return err
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if len(raw) > operationcontrol.MaxHelperIntent {
		return errCapacity
	}
	var prior string
	err = r.db.QueryRow(`SELECT invocation FROM operation_helpers WHERE owner=? AND id=? AND kind=? AND key=?`, i.Target.UID, i.Target.RequestID, h.Kind, h.Key).Scan(&prior)
	if err == nil {
		old, e := helperRecord(prior, i)
		if e != nil {
			return e
		}
		if old != h {
			return errConflict
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	var count int
	if err = r.db.QueryRow(`SELECT count(*) FROM operation_helpers WHERE owner=? AND id=?`, i.Target.UID, i.Target.RequestID).Scan(&count); err != nil {
		return err
	}
	if count >= operationcontrol.MaxHelpers {
		return errCapacity
	}
	result, err := r.db.Exec(`INSERT INTO operation_helpers(owner,id,kind,key,invocation) VALUES(?,?,?,?,?)`, i.Target.UID, i.Target.RequestID, h.Kind, h.Key, string(raw))
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
	if err = r.db.QueryRow(`SELECT invocation FROM operation_helpers WHERE owner=? AND id=? AND kind=? AND key=?`, i.Target.UID, i.Target.RequestID, h.Kind, h.Key).Scan(&prior); err != nil {
		return err
	}
	if prior != string(raw) {
		return errInDoubt
	}
	return nil
}
func (r *receipts) operationHelpers(i operationcontrol.Intent) ([]operationcontrol.Helper, error) {
	stored, err := r.operationIntent(i.Target)
	if err != nil {
		return nil, err
	}
	if stored != i {
		return nil, errConflict
	}
	if err = r.requireHelperTracking(i); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(`SELECT kind,key,invocation FROM operation_helpers WHERE owner=? AND id=? ORDER BY kind,key`, i.Target.UID, i.Target.RequestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []operationcontrol.Helper
	for rows.Next() {
		if len(out) >= operationcontrol.MaxHelpers {
			return nil, errInDoubt
		}
		var kind, key, raw string
		if err = rows.Scan(&kind, &key, &raw); err != nil {
			return nil, err
		}
		h, err := helperRecord(raw, i)
		if err != nil {
			return nil, err
		}
		if h.Kind != kind || h.Key != key {
			return nil, errInDoubt
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// An empty helper inventory is meaningful only when this guarantee was pinned
// at initial operation admission. Migration never upgrades old activity into
// tracked activity merely because it has no recorded helper rows.
func (r *receipts) requireHelperTracking(i operationcontrol.Intent) error {
	var epoch string
	var tracking int
	if err := r.db.QueryRow(`SELECT epoch,helper_tracking FROM operation_intents WHERE owner=? AND id=? AND digest=?`, i.Target.UID, i.Target.RequestID, i.Target.Digest).Scan(&epoch, &tracking); err != nil {
		return errInDoubt
	}
	if epoch != i.Epoch || tracking != 1 {
		return errInDoubt
	}
	return nil
}
func (r *receipts) retainedHelpers(uid uint32, id string) (bool, error) {
	var count int
	err := r.db.QueryRow(`SELECT count(*) FROM operation_helpers WHERE owner=? AND id=?`, uid, id).Scan(&count)
	return count != 0, err
}

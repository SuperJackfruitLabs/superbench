package operationreceipts

import (
	"database/sql"
	"encoding/json"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

type storedReceipt struct {
	target         operationcontrol.Target
	state, barrier string
	body           []byte
	status         sql.NullInt64
}

// Historical replay is evidence, not a new admission. Validate its immutable
// relation before returning a body; malformed retained rows never become absent.
func (r *receipts) validateReceipt(v storedReceipt) error {
	if v.barrier != "" && v.barrier != "cancel_requested" && v.barrier != "cancelled" {
		return errInDoubt
	}
	switch v.state {
	case "done":
		if v.barrier != "" || !v.status.Valid || v.status.Int64 < 100 || v.status.Int64 > 599 {
			return errInDoubt
		}
	case "running", "interrupted":
		if v.status.Valid || v.body != nil || v.state == "running" && v.barrier == "cancelled" {
			return errInDoubt
		}
	default:
		return errInDoubt
	}
	i, err := r.operationIntent(v.target)
	if errors.Is(err, sql.ErrNoRows) {
		if v.barrier == "cancelled" {
			return errInDoubt
		}
		return nil
	}
	if err != nil {
		return errInDoubt
	}
	// Legacy untyped receipts retain their original opaque bytes (including
	// diagnostic evidence rejected by the bounded public response adapter).
	limit, err := r.responseLimit(r.db, v.target.UID, v.target.RequestID)
	if err != nil {
		return errInDoubt
	}
	if v.state == "done" && (v.body == nil || int64(len(v.body)) > limit) {
		return errInDoubt
	}
	var finalized, tracked int
	if err = r.db.QueryRow(`SELECT core_finalized,helper_tracking FROM operation_intents WHERE owner=? AND id=?`, i.Target.UID, i.Target.RequestID).Scan(&finalized, &tracked); err != nil {
		return err
	}
	if finalized < 0 || finalized > 1 || tracked < 0 || tracked > 1 {
		return errInDoubt
	}
	if finalized == 1 {
		done := v.state == "done" && v.barrier == "" && json.Valid(v.body)
		cancelled := v.state == "interrupted" && v.barrier == "cancelled" && tracked == 1
		if !done && !cancelled {
			return errInDoubt
		}
	} else if v.barrier == "cancelled" {
		return errInDoubt
	}
	return nil
}

// Open validates all retained relationships in bounded pages. Reconciliation
// may then use its unresolved index instead of rescanning terminal lifetime
// history every second. Exact lookup still revalidates the selected record.
func (r *receipts) validateHistory() error {
	for _, table := range []string{"receipts", "operation_intents", "operation_helpers"} {
		if err := r.validatePaginationKeys(table); err != nil {
			return err
		}
	}
	var invalid int
	if err := r.db.QueryRow(`SELECT count(*) FROM operation_intents i LEFT JOIN receipts r ON r.owner=i.owner AND r.id=i.id WHERE r.id IS NULL OR r.digest<>i.digest`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return errInDoubt
	}
	if err := r.db.QueryRow(`SELECT count(*) FROM operation_helpers h LEFT JOIN operation_intents i ON i.owner=h.owner AND i.id=h.id WHERE i.id IS NULL`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return errInDoubt
	}
	if err := r.validateResponseLimits(); err != nil {
		return err
	}
	cursor := WorkCursor{}
	for {
		rows, err := r.db.Query(r.historyBodies(), cursor.UID, cursor.UID, cursor.RequestID)
		if err != nil {
			return err
		}
		var page []storedReceipt
		for rows.Next() {
			var v storedReceipt
			var uid int64
			if err = rows.Scan(&uid, &v.target.RequestID, &v.target.Digest, &v.state, &v.barrier, &v.body, &v.status); err != nil {
				rows.Close()
				return err
			}
			if uid < 0 || uid > 1<<32-1 {
				rows.Close()
				return errInDoubt
			}
			v.target.UID = uint32(uid)
			page = append(page, v)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		for _, v := range page {
			if err = r.validateReceipt(v); err != nil {
				return err
			}
			cursor = WorkCursor{v.target.UID, v.target.RequestID}
		}
		if len(page) < 64 {
			break
		}
	}
	return r.validateTombstones()
}

func (r *receipts) validateTombstones() error {
	if err := r.validatePaginationKeys("operation_cancels"); err != nil {
		return err
	}
	cursor := WorkCursor{}
	for {
		rows, err := r.db.Query(`SELECT owner,id,digest,state FROM operation_cancels WHERE owner>? OR (owner=? AND id>?) ORDER BY owner,id LIMIT 64`, cursor.UID, cursor.UID, cursor.RequestID)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			var target operationcontrol.Target
			var uid int64
			var state string
			if err = rows.Scan(&uid, &target.RequestID, &target.Digest, &state); err != nil {
				rows.Close()
				return err
			}
			if uid < 0 || uid > 1<<32-1 {
				rows.Close()
				return errInDoubt
			}
			target.UID = uint32(uid)
			if !target.Valid() || state != "cancel_requested" && state != "cancelled" {
				rows.Close()
				return errInDoubt
			}
			cursor = WorkCursor{target.UID, target.RequestID}
			count++
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if count < 64 {
			return nil
		}
	}
}

// Reject invalid keys before applying the unsigned keyset cursor. Otherwise a
// negative owner or (root UID, empty ID) would sort before the first page and
// escape both relationship validation and all later continuation pages.
func (r *receipts) validatePaginationKeys(table string) error {
	switch table {
	case "receipts", "operation_intents", "operation_helpers", "operation_cancels":
	default:
		return errInDoubt
	}
	var invalid int
	err := r.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM ` + table + ` WHERE typeof(owner)<>'integer' OR owner<0 OR owner>4294967295 OR typeof(id)<>'text' OR length(CAST(id AS BLOB))=0)`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid != 0 {
		return errInDoubt
	}
	return nil
}

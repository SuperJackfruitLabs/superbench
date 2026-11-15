package operationreceipts

import (
	"database/sql"
	"regexp"
)

const maxCancelTombstones = 1024

var cancelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var cancelDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type cancelReader interface{ QueryRow(string, ...any) *sql.Row }

// The private primitive records intent only. No public endpoint or "cancelled"
// transition is enabled before core authority and quiescence checks are wired.
func (r *receipts) requestCancel(uid uint32, id, digest string) (string, error) {
	if !cancelID.MatchString(id) || !cancelDigest.MatchString(digest) {
		return "", errConflict
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requestCancelLocked(uid, id, digest)
}
func (r *receipts) requestCancelLocked(uid uint32, id, digest string) (string, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var prior, state, cancelled string
	err = tx.QueryRow(`SELECT digest,state,cancel_state FROM receipts WHERE owner=? AND id=?`, uid, id).Scan(&prior, &state, &cancelled)
	if err == nil {
		if prior != digest {
			return "", errConflict
		}
		if state == "done" {
			return "already_completed", nil
		}
		if state != "running" && state != "interrupted" {
			return "", errInDoubt
		}
		switch cancelled {
		case "cancelled":
			return "cancelled", nil
		case "", "cancel_requested":
		default:
			return "", errInDoubt
		}
		result, err := tx.Exec(`UPDATE receipts SET cancel_state='cancel_requested' WHERE owner=? AND id=? AND digest=? AND state<>'done'`, uid, id, digest)
		if err != nil {
			return "", err
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			if e != nil {
				return "", e
			}
			return "", errInDoubt
		}
		if err = tx.Commit(); err != nil {
			return "", err
		}
		if stop := r.controllers[receiptKey(uid, id)]; stop != nil {
			stop()
		}
		return "cancel_requested", nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	if _, known := r.active[receiptKey(uid, id)]; known {
		return "", errInDoubt
	}
	err = tx.QueryRow(`SELECT digest,state FROM operation_cancels WHERE owner=? AND id=?`, uid, id).Scan(&prior, &state)
	if err == nil {
		if prior != digest {
			return "", errConflict
		}
		if state != "cancel_requested" && state != "cancelled" {
			return "", errInDoubt
		}
		return state, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM operation_cancels`).Scan(&count); err != nil {
		return "", err
	}
	if count >= maxCancelTombstones {
		return "", errCapacity
	}
	result, err := tx.Exec(`INSERT INTO operation_cancels(owner,id,digest,state) VALUES (?,?,?,'cancel_requested')`, uid, id, digest)
	if err != nil {
		return "", err
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		if e != nil {
			return "", e
		}
		return "", errInDoubt
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return "cancel_requested", nil
}
func cancelBarrier(db cancelReader, uid uint32, id, digest string) error {
	var prior string
	err := db.QueryRow(`SELECT digest FROM operation_cancels WHERE owner=? AND id=?`, uid, id).Scan(&prior)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if prior != digest {
		return errConflict
	}
	return errCancelRequested
}
func admittedCancelBarrier(db cancelReader, uid uint32, id string) error {
	var state string
	err := db.QueryRow(`SELECT cancel_state FROM receipts WHERE owner=? AND id=?`, uid, id).Scan(&state)
	if err == sql.ErrNoRows {
		return errInDoubt
	}
	if err != nil {
		return err
	}
	if state != "" {
		return errCancelRequested
	}
	return nil
}

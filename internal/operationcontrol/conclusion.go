package operationcontrol

import (
	"database/sql"
	"encoding/json"
)

// CancellationConcluded recognizes only a prior, durably verified cancellation
// of this exact epoch. A normal finished operation has no such authority.
func CancellationConcluded(r Reader, i Intent) (bool, error) {
	if !i.Valid() {
		return false, ErrAuthority
	}
	b, err := Get(r, i.Target)
	if err != nil {
		return false, err
	}
	if b.Intent != i {
		return false, ErrAuthority
	}
	var raw sql.NullString
	err = r.QueryRow(`SELECT CASE WHEN length(CAST(record AS BLOB))<=65536 THEN record ELSE NULL END FROM workspace_operation_cancellations WHERE operation_id=?`, i.Target.ID()).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expected, err := json.Marshal(b)
	if err != nil {
		return false, err
	}
	if (b.Phase != "finished" && b.Phase != "stopped") || !raw.Valid || raw.String != string(expected) {
		return false, ErrAuthority
	}
	return true, nil
}

func saveCancellationConclusion(tx *sql.Tx, i Intent) error {
	b, err := Get(tx, i.Target)
	if err != nil {
		return err
	}
	if b.Intent != i || b.Phase != "finished" && b.Phase != "stopped" {
		return ErrAuthority
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if len(raw) > 64<<10 {
		return ErrAuthority
	}
	result, err := tx.Exec(`INSERT INTO workspace_operation_cancellations(operation_id,record) VALUES(?,?)`, i.Target.ID(), string(raw))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAuthority
	}
	ok, err := CancellationConcluded(tx, i)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAuthority
	}
	return nil
}

// RejectCancellationAuthority prevents historical schemas from adopting even
// empty future authority tables. No terminal conclusion is inferred on upgrade.
func RejectCancellationAuthority(r Reader) error {
	var count int
	if err := r.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workspace_operation_cancellations'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrAuthority
	}
	return nil
}

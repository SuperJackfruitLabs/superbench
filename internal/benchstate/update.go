package benchstate

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Update validates one callback transition and writes its records in the
// caller's transaction. The caller retains migration and host-lock authority.
func Update(tx *sql.Tx, fn func(Database, *sql.Tx) error) error {
	records, err := Load(tx)
	if err != nil {
		return err
	}
	before := Capture(records)
	if err = fn(records, tx); err != nil {
		return err
	}
	if err = ValidateTransition(before, records); err != nil {
		return err
	}
	if err = Validate(records); err != nil {
		return err
	}
	for key, b := range records {
		raw, e := json.Marshal(b)
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO benches(key, record) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET record=excluded.record WHERE record != excluded.record", key, string(raw)); e != nil {
			return e
		}
		var stored string
		if e = tx.QueryRow("SELECT record FROM benches WHERE key=?", key).Scan(&stored); e != nil {
			return e
		}
		if stored != string(raw) {
			return errors.New("core transition was not durably retained")
		}

	}
	return nil
}

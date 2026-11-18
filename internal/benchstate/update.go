package benchstate

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffguard"
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
	// Whatever path releases a workspace's writer, the release starts its idle
	// grace in the same transaction, so no released record is older than it.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for key, old := range before {
		if b := records[key]; old.lease != nil && b != nil && b.Workspace != nil && b.Lease == nil {
			b.Workspace.IdleSince = now
		}
	}
	if err = ValidateTransition(before, records); err != nil {
		return err
	}
	if err = Validate(records); err != nil {
		return err
	}
	for key, b := range records {
		if err = handoffguard.Advance(tx, b); err != nil {
			return err
		}
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

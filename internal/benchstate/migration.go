package benchstate

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

func CheckVersion(r Reader) error {
	var v int
	if err := r.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v != SchemaVersion {
		return fmt.Errorf("unsupported state schema %d (expected %d)", v, SchemaVersion)
	}
	return nil
}
func Migrate(tx *sql.Tx, state string, pinMigratedInodes func(*model.Bench) error) error {
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == SchemaVersion {
		return nil
	}
	if version >= 1 && version <= 16 {
		records, err := Load(tx)
		if err != nil {
			return err
		}
		for _, b := range records {
			if b.Workspace != nil && (version < 14 || version == 14 && workspacepolicy.ValidateSchema14(b) != nil || version == 15 && workspacepolicy.ValidateSchema15(b) != nil || version == 16 && workspacepolicy.ValidateSchema16(b) != nil) {
				return errors.New("aggregate workspace cannot be adopted from an older state schema")
			}
			if b.Lease != nil && b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped" {
				return errors.New("stop supervised executions with the installed binary before upgrading state")
			}
		}
		for key, b := range records {
			if version < 5 {
				if err = pinMigratedInodes(b); err != nil {
					return err
				}
			}
			if b.ArchivedAt != "" && b.ReapAfter == "" {
				b.ReapAfter, err = archivepolicy.Deadline(b.ArchivedAt, archivepolicy.DefaultGrace)
				if err != nil {
					return err
				}
			}
			raw, e := json.Marshal(b)
			if e != nil {
				return e
			}
			if _, e = tx.Exec("UPDATE benches SET record=? WHERE key=?", string(raw), key); e != nil {
				return e
			}
		}
		if err = history.Schema(tx); err != nil {
			return err
		}
		if err = setup.Schema(tx); err != nil {
			return err
		}
		_, err = tx.Exec("PRAGMA user_version=17")
		return err
	}
	if version != 0 {
		return fmt.Errorf("unsupported state schema %d", version)
	}
	records, err := Legacy(state)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("CREATE TABLE benches (key TEXT PRIMARY KEY, record TEXT NOT NULL CHECK(json_valid(record)))"); err != nil {
		return err
	}
	for key, b := range records {
		if b.Workspace != nil {
			return errors.New("aggregate workspace cannot be adopted from legacy state")
		}
		if b.ArchivedAt != "" && b.ReapAfter == "" {
			b.ReapAfter, err = archivepolicy.Deadline(b.ArchivedAt, archivepolicy.DefaultGrace)
			if err != nil {
				return err
			}
		}
		raw, e := json.Marshal(b)
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO benches(key,record) VALUES (?,?)", key, string(raw)); e != nil {
			return e
		}
	}
	if err = history.Schema(tx); err != nil {
		return err
	}
	if err = setup.Schema(tx); err != nil {
		return err
	}
	_, err = tx.Exec("PRAGMA user_version=17")
	return err
}

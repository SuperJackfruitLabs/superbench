package benchstate

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/initrepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
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
	if version >= 1 && version <= 22 {
		if err := historicalOperations(tx); err != nil {
			return err
		}
		records, err := Load(tx)
		if err != nil {
			return err
		}
		for _, b := range records {
			if b.Workspace != nil && (version < 14 || version == 14 && workspacepolicy.ValidateSchema14(b) != nil || version == 15 && workspacepolicy.ValidateSchema15(b) != nil || version == 16 && workspacepolicy.ValidateSchema16(b) != nil || version == 17 && workspacepolicy.ValidateSchema17(b) != nil || (version == 18 || version == 19) && workspacepolicy.ValidateSchema19(b) != nil || version == 20 && workspacepolicy.ValidateSchema20(b) != nil || version == 21 && workspacepolicy.ValidateSchema21(b) != nil) {
				return errors.New("aggregate workspace cannot be adopted from an older state schema")
			}
			if b.Lease != nil && b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped" {
				return errors.New("stop supervised executions with the installed binary before upgrading state")
			}
		}
		if version < 19 {
			if err := historicalHistory(tx); err != nil {
				return err
			}
		}
		if version < 20 {
			if err := historicalRepairs(tx); err != nil {
				return err
			}
		}
		if version < 21 {
			if err := historicalTransfers(tx); err != nil {
				return err
			}
		} else if err := historicalTransferPlans(tx); err != nil {
			return err
		}
		for key, b := range records {
			if version >= 18 {
				continue // Preserve current records byte-for-byte; only authority changes.
			}
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
		if err = initrepair.Schema(tx); err != nil {
			return err
		}
		if err = reftransfer.Schema(tx); err != nil {
			return err
		}
		if err = operationcontrol.Schema(tx); err != nil {
			return err
		}
		_, err = tx.Exec("PRAGMA user_version=23")
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
	if err = initrepair.Schema(tx); err != nil {
		return err
	}
	if err = reftransfer.Schema(tx); err != nil {
		return err
	}
	if err = operationcontrol.Schema(tx); err != nil {
		return err
	}
	_, err = tx.Exec("PRAGMA user_version=23")
	return err
}

func historicalTransfers(r Reader) error {
	var count int
	if err := r.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workspace_ref_transfers'").Scan(&count); err != nil || count == 0 {
		return err
	}
	if err := r.QueryRow("SELECT count(*) FROM workspace_ref_transfers").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("task-ref transfer journal requires current state schema")
	}
	return nil
}

func historicalRepairs(r Reader) error {
	var count int
	if err := r.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='initialization_repairs'").Scan(&count); err != nil || count == 0 {
		return err
	}
	if err := r.QueryRow("SELECT count(*) FROM initialization_repairs").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("initialization repair journal requires current state schema")
	}
	return nil
}

// Older schemas cannot authorize interrupted-history observations, even if a
// record has been injected with a structurally valid future observation.
func historicalHistory(r Reader) error {
	var count int
	if err := r.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='run_history'").Scan(&count); err != nil || count == 0 {
		return err
	}
	if err := r.QueryRow("SELECT count(*) FROM run_history WHERE json_type(record,'$.Interruption') IS NOT NULL AND json_type(record,'$.Interruption') != 'null'").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("interrupted history requires current state schema")
	}
	return nil
}

// An older schema cannot claim operation provenance or cancellation authority.
func historicalOperations(r Reader) error {
	var n int
	if err := r.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workspace_operations'").Scan(&n); err != nil || n == 0 {
		return err
	}
	if err := r.QueryRow("SELECT count(*) FROM workspace_operations").Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return errors.New("operation bindings require current state schema")
	}
	return nil
}

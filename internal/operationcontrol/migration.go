package operationcontrol

import (
	"database/sql"
)

type LegacyReader interface {
	Reader
	Query(string, ...any) (*sql.Rows, error)
}

// ValidateSchema23 refuses future authority in an older schema. Legitimate old
// bindings have no control provenance and can only migrate as cooperative.
func RejectRunAuthority(r Reader) error {
	var count int
	if err := r.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workspace_operation_runs'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrAuthority
	}
	return nil
}
func ValidateSchema23(r LegacyReader) error { return validateHistoricalBindings(r, false) }
func ValidateSchema24(r LegacyReader) error { return validateHistoricalBindings(r, true) }
func validateHistoricalBindings(r LegacyReader, withControl bool) error {
	if err := RejectRunAuthority(r); err != nil {
		return err
	}
	var exists int
	if err := r.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workspace_operations'`).Scan(&exists); err != nil || exists == 0 {
		return err
	}
	var header string
	if err := r.QueryRow(`SELECT group_concat(name,',') FROM (SELECT name FROM pragma_table_info('workspace_operations') ORDER BY cid)`).Scan(&header); err != nil {
		return err
	}
	expected := legacyColumns
	if withControl {
		expected = columns
	}
	if header != "id,"+expected {
		return ErrAuthority
	}
	rows, err := r.Query(`SELECT id,` + expected + ` FROM workspace_operations`)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen, active := map[string]bool{}, map[string]bool{}
	count := 0
	for rows.Next() {
		count++
		if count > MaxOperations {
			return ErrAuthority
		}
		b, err := scanBinding(rows, withControl)
		if err != nil {
			return err
		}
		id := b.Intent.Target.ID()
		if seen[id] {
			return ErrAuthority
		}
		seen[id] = true
		if b.Phase == "admitted" || b.Phase == "bound" {
			if active[b.Intent.Key] {
				return ErrAuthority
			}
			active[b.Intent.Key] = true
		}
	}
	return rows.Err()
}
func UpgradeSchema23(tx *sql.Tx) error {
	if err := ValidateSchema23(tx); err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workspace_operations'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		if _, err := tx.Exec(`ALTER TABLE workspace_operations ADD COLUMN control_state TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_operations ADD COLUMN control_root TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_operations ADD COLUMN control_host TEXT NOT NULL DEFAULT '';
ALTER TABLE workspace_operations ADD COLUMN control_boot TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return Schema(tx)
}

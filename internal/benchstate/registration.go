package benchstate

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func validRegistration(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func registrationIdentities(records Database, current bool) error {
	seen := map[string]bool{}
	for _, b := range records {
		if b == nil {
			continue
		}
		if err := model.ValidateScalarLayout(b); err != nil {
			return err
		}
		id := b.RegistrationID
		if b.Workspace != nil {
			if id != "" {
				return errors.New("aggregate identity must use Workspace.ID")
			}
			id = b.Workspace.ID
		} else if !current {
			if id != "" {
				return errors.New("scalar registration identity requires current state schema")
			}
			continue
		}
		if !validRegistration(id) || seen[id] {
			return errors.New("invalid or duplicate bench registration identity")
		}
		seen[id] = true
	}
	return nil
}

// Only the explicit upgrade owns assignment. Aggregate and all other tables
// remain byte-identical; scalar JSON gains exactly one new identity field.
func migrateRegistrationIDs(tx *sql.Tx, records Database) error {
	if err := rejectHistoricalRegistration(tx); err != nil {
		return err
	}
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b := records[key]
		if b.Workspace != nil {
			continue
		}
		id, err := model.Nonce()
		if err != nil {
			return err
		}
		var raw string
		if err = tx.QueryRow("SELECT record FROM benches WHERE key=?", key).Scan(&raw); err != nil {
			return err
		}
		end := strings.LastIndex(raw, "}")
		if end < 0 {
			return errors.New("invalid scalar record")
		}
		updated := raw[:end] + `,"RegistrationID":"` + id + `"` + raw[end:]
		if _, err = tx.Exec("UPDATE benches SET record=? WHERE key=?", updated, key); err != nil {
			return err
		}
		var stored string
		if err = tx.QueryRow("SELECT record FROM benches WHERE key=?", key).Scan(&stored); err != nil {
			return err
		}
		if stored != updated {
			return errors.New("registration migration was not durably retained")
		}
		b.RegistrationID = id
	}
	return registrationIdentities(records, true)
}

func rejectHistoricalRegistration(r Reader) error {
	var n int
	if err := r.QueryRow("SELECT count(*) FROM benches WHERE json_type(record,'$.RegistrationID') IS NOT NULL OR json_type(record,'$.PayloadLayoutVersion') IS NOT NULL OR json_type(record,'$.AllocationRoot') IS NOT NULL").Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return errors.New("historical state contains future registration identity")
	}
	return nil
}
func legacyRegistrationFields(raw []byte) error {
	var rows map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return err
	}
	for _, b := range rows {
		if _, ok := b["PayloadLayoutVersion"]; ok {
			return errors.New("legacy state contains future scalar layout")
		}
		if _, ok := b["AllocationRoot"]; ok {
			return errors.New("legacy state contains future scalar layout")
		}
		if _, exists := b["RegistrationID"]; exists {
			return errors.New("legacy state contains future registration identity")
		}
	}
	return nil
}

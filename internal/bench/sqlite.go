package bench

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schemaVersion = 13

type reader interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func (s Store) openSQL(readOnly bool) (*sql.DB, error) {
	path := filepath.Join(s.State, "bench.db")
	if !readOnly {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	if readOnly {
		q.Set("mode", "ro")
	} else {
		q.Set("mode", "rw")
	}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func checkVersion(r reader) error {
	var v int
	if err := r.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v != schemaVersion {
		return fmt.Errorf("unsupported state schema %d (expected %d)", v, schemaVersion)
	}
	return nil
}
func validRecords(records database) error {
	if records == nil {
		return errors.New("invalid state: null database")
	}
	for key, b := range records {
		if b != nil {
			if b.CreatedFrom != "" && !model.ValidCommit(b.CreatedFrom) {
				return errors.New("invalid creation source commit")
			}
			if !quotapolicy.ValidBytes(b.RequestedQuotaBytes) || b.ProjectID != 0 && b.RequestedQuotaBytes != 0 && b.RequestedQuotaBytes != b.QuotaBytes {
				return errors.New("invalid pinned quota request")
			}
			if e := archivepolicy.ValidateArchive(b); e != nil {
				return e
			}
		}
		if b != nil && b.Lease != nil {
			if len(b.Lease.RetainedRuns) > 8 || (b.Lease.Continuation != "" && !history.ID.MatchString(b.Lease.Continuation)) {
				return errors.New("invalid preparation chain")
			}
			seen := map[string]bool{}
			for _, id := range b.Lease.RetainedRuns {
				if !history.ID.MatchString(id) || seen[id] || id == executionID(b.Lease) {
					return errors.New("invalid retained preparation history")
				}
				seen[id] = true
			}
		}

		if b == nil || b.Key != key || b.Path == "" || b.Mirror == "" {
			return errors.New("invalid bench record")
		}
		if b.ArchivedAt != "" && b.Lease != nil || b.ReapingDigest != "" && b.ArchivedAt == "" || b.ReapedAt != "" && b.ReapingDigest == "" {
			return errors.New("invalid bench record")
		}
	}
	return nil
}
func (s Store) legacy() (database, error) {
	records := database{}
	raw, err := os.ReadFile(filepath.Join(s.State, "benches.json"))
	if os.IsNotExist(err) {
		return records, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &records); err != nil {
		return nil, fmt.Errorf("invalid legacy state: %w", err)
	}
	if err = validRecords(records); err != nil {
		return nil, err
	}
	return records, nil
}
func (s Store) migrate(tx *sql.Tx) error {
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == schemaVersion {
		return nil
	}
	if version >= 1 && version <= 12 {
		records, err := loadRecords(tx)
		if err != nil {
			return err
		}
		for _, b := range records {
			if b.Lease != nil && b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped" {
				return errors.New("stop supervised executions with the installed binary before upgrading state")
			}
		}
		for key, b := range records {
			if version < 5 {
				if err = s.pinMigratedInodes(b); err != nil {
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
		_, err = tx.Exec("PRAGMA user_version=13")
		return err
	}
	if version != 0 {
		return fmt.Errorf("unsupported state schema %d", version)
	}
	records, err := s.legacy()
	if err != nil {
		return err
	}
	if _, err = tx.Exec("CREATE TABLE benches (key TEXT PRIMARY KEY, record TEXT NOT NULL CHECK(json_valid(record)))"); err != nil {
		return err
	}
	for key, b := range records {
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
	_, err = tx.Exec("PRAGMA user_version=13")
	return err
}
func loadRecords(r reader) (database, error) {
	rows, err := r.Query("SELECT key,record FROM benches ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := database{}
	for rows.Next() {
		var key, raw string
		if err = rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var b *Bench
		if err = json.Unmarshal([]byte(raw), &b); err != nil {
			return nil, err
		}
		records[key] = b
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = validRecords(records); err != nil {
		return nil, err
	}
	return records, nil
}

func (s Store) Migrate() error { return s.transaction(func(database) error { return nil }) }

// Package benchstate validates and migrates authoritative bench records.
package benchstate

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

const SchemaVersion = 21

type Database map[string]*model.Bench

type Reader interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func Validate(records Database) error {
	if records == nil {
		return errors.New("invalid state: null database")
	}
	for key, b := range records {
		if b != nil && b.Workspace != nil {
			if b.Key != key {
				return errors.New("invalid bench record")
			}
			if err := workspacepolicy.Validate(b); err != nil {
				return err
			}
			continue
		}
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
	return validateTransferLinks(records)
}
func Legacy(state string) (Database, error) {
	records := Database{}
	raw, err := os.ReadFile(filepath.Join(state, "benches.json"))
	if os.IsNotExist(err) {
		return records, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &records); err != nil {
		return nil, fmt.Errorf("invalid legacy state: %w", err)
	}
	if err = Validate(records); err != nil {
		return nil, err
	}
	return records, nil
}
func Load(r Reader) (Database, error) {
	rows, err := r.Query("SELECT key,record FROM benches ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := Database{}
	for rows.Next() {
		var key, raw string
		if err = rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var b *model.Bench
		if err = json.Unmarshal([]byte(raw), &b); err != nil {
			return nil, err
		}
		records[key] = b
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = Validate(records); err != nil {
		return nil, err
	}
	return records, nil
}

func executionID(lease *model.Lease) string {
	if lease.Execution != nil {
		return lease.Execution.ID
	}
	return ""
}

// Package reftransfer journals irreversible original task-ref handoffs. It owns
// approved bytes and monotonic progress, not Git, leases or workspace authority.
package reftransfer

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

const MaxRecords = 10000
const MaxPlan = 16 << 20

var identity = regexp.MustCompile(`^[a-f0-9]{32}$`)
var requestID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Reader interface{ QueryRow(string, ...any) *sql.Row }
type Record struct {
	ID, SourceID, SourceKey, TargetID, TargetKey, CreationID, Digest string
	Plan                                                             json.RawMessage
	CreatedAt                                                        time.Time
	Phase                                                            string
}

func Schema(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS workspace_ref_transfers (
 id TEXT PRIMARY KEY,source_id TEXT NOT NULL UNIQUE,source_key TEXT NOT NULL UNIQUE,
 target_id TEXT NOT NULL UNIQUE,target_key TEXT NOT NULL UNIQUE,creation_id TEXT NOT NULL,
 digest TEXT NOT NULL,plan BLOB NOT NULL,created TEXT NOT NULL,
 phase TEXT NOT NULL CHECK(phase IN ('started','relinquished','completed')));`)
	return err
}
func valid(r Record) error {
	_, offset := r.CreatedAt.Zone()
	if !requestID.MatchString(r.ID) || !identity.MatchString(r.SourceID) || !identity.MatchString(r.TargetID) || r.SourceID == r.TargetID || !requestID.MatchString(r.SourceKey) || !requestID.MatchString(r.TargetKey) || r.SourceKey == r.TargetKey || !requestID.MatchString(r.CreationID) || !digest.MatchString(r.Digest) || r.CreatedAt.IsZero() || offset != 0 || len(r.Plan) == 0 || len(r.Plan) > MaxPlan || !json.Valid(r.Plan) || r.Phase != "started" && r.Phase != "relinquished" && r.Phase != "completed" {
		return errors.New("invalid task-ref transfer journal")
	}
	d := json.NewDecoder(bytes.NewReader(r.Plan))
	d.UseNumber()
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("task-ref transfer approval must be an object")
	}
	return approvedObject(d)
}
func approvedObject(d *json.Decoder) error {
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return errors.New("ambiguous transfer plan fields")
		}
		seen[name] = true
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "_", ""), "-", ""))
		switch normalized {
		case "token", "leasetoken", "continuation", "arguments", "environment", "args", "env":
			return errors.New("transfer plan contains capabilities or command inputs")
		}
		if err := approvedValue(d); err != nil {
			return err
		}
	}
	_, err := d.Token()
	return err
}
func approvedValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		return approvedObject(d)
	case json.Delim('['):
		for d.More() {
			if err := approvedValue(d); err != nil {
				return err
			}
		}
		_, err := d.Token()
		return err
	}
	return nil
}
func Get(db Reader, id string) (Record, error) {
	var r Record
	var created string
	if !requestID.MatchString(id) {
		return r, errors.New("invalid transfer journal identity")
	}
	if err := db.QueryRow("SELECT id,source_id,source_key,target_id,target_key,creation_id,digest,plan,created,phase FROM workspace_ref_transfers WHERE id=?", id).Scan(&r.ID, &r.SourceID, &r.SourceKey, &r.TargetID, &r.TargetKey, &r.CreationID, &r.Digest, &r.Plan, &created, &r.Phase); err != nil {
		return Record{}, err
	}
	var err error
	r.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Record{}, err
	}
	if err := valid(r); err != nil {
		return Record{}, err
	}
	return r, nil
}
func sameApproval(a, b Record) bool {
	return a.ID == b.ID && a.SourceID == b.SourceID && a.SourceKey == b.SourceKey && a.TargetID == b.TargetID && a.TargetKey == b.TargetKey && a.CreationID == b.CreationID && a.Digest == b.Digest && bytes.Equal(a.Plan, b.Plan) && a.CreatedAt.Equal(b.CreatedAt)
}
func Start(tx *sql.Tx, r Record) error {
	if err := valid(r); err != nil {
		return err
	}
	if r.Phase != "started" {
		return errors.New("new transfer must begin started")
	}
	prior, err := Get(tx, r.ID)
	if err == nil {
		if prior.Phase == "started" && sameApproval(prior, r) {
			return nil
		}
		return errors.New("transfer identity already journaled")
	}
	if err != sql.ErrNoRows {
		return err
	}
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM workspace_ref_transfers").Scan(&count); err != nil {
		return err
	}
	if count >= MaxRecords {
		return errors.New("task-ref transfer journal budget exhausted")
	}
	_, err = tx.Exec("INSERT INTO workspace_ref_transfers(id,source_id,source_key,target_id,target_key,creation_id,digest,plan,created,phase) VALUES(?,?,?,?,?,?,?,?,?,?)", r.ID, r.SourceID, r.SourceKey, r.TargetID, r.TargetKey, r.CreationID, r.Digest, []byte(r.Plan), r.CreatedAt.Format(time.RFC3339Nano), r.Phase)
	return err
}
func Relinquish(tx *sql.Tx, id, approval string) error {
	return advance(tx, id, approval, "started", "relinquished")
}
func Complete(tx *sql.Tx, id, approval string) error {
	return advance(tx, id, approval, "relinquished", "completed")
}
func advance(tx *sql.Tx, id, approval, previous, next string) error {
	r, err := Get(tx, id)
	if err != nil {
		return err
	}
	if r.Digest != approval {
		return errors.New("transfer approval changed")
	}
	if r.Phase == next {
		return nil
	}
	if r.Phase != previous {
		return errors.New("task-ref transfer progress cannot skip or reverse phases")
	}
	result, err := tx.Exec("UPDATE workspace_ref_transfers SET phase=? WHERE id=? AND phase=?", next, id, previous)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("transfer journal changed during progress")
	}
	return err
}

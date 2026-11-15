// Package initrepair journals explicit initialization repair approvals without
// holding writer capabilities or owning workspace transition authority.
package initrepair

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
	BenchID, ID, Key, Digest, PreviousID, OwnerDigest string
	Plan                                              json.RawMessage
	CreatedAt                                         time.Time
	Status                                            string
}

func Schema(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS initialization_repairs (
 bench_id TEXT NOT NULL,id TEXT NOT NULL,key TEXT NOT NULL,digest TEXT NOT NULL,
 previous_id TEXT NOT NULL,owner_digest TEXT NOT NULL,plan BLOB NOT NULL,created TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('active','completed','superseded')),
 PRIMARY KEY(bench_id,id));
 CREATE UNIQUE INDEX IF NOT EXISTS initialization_repair_active ON initialization_repairs(bench_id) WHERE status='active';`)
	return err
}

func valid(r Record) error {
	_, offset := r.CreatedAt.Zone()
	if !identity.MatchString(r.BenchID) || !requestID.MatchString(r.ID) || !requestID.MatchString(r.Key) || !digest.MatchString(r.Digest) || !digest.MatchString(r.OwnerDigest) || r.PreviousID != "" && (!requestID.MatchString(r.PreviousID) || r.PreviousID == r.ID) || r.CreatedAt.IsZero() || offset != 0 || len(r.Plan) == 0 || len(r.Plan) > MaxPlan || !json.Valid(r.Plan) || r.Status != "active" && r.Status != "completed" && r.Status != "superseded" {
		return errors.New("invalid initialization repair journal")
	}
	d := json.NewDecoder(bytes.NewReader(r.Plan))
	d.UseNumber()
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("initialization repair plan must be an object without capabilities or command inputs")
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
			return errors.New("ambiguous initialization repair plan fields")
		}
		seen[name] = true
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "_", ""), "-", ""))
		switch normalized {
		case "token", "leasetoken", "continuation", "arguments", "environment", "args", "env":
			return errors.New("repair plan contains capabilities or command inputs")
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

func Get(db Reader, benchID, id string) (Record, error) {
	var r Record
	var created string
	if !identity.MatchString(benchID) || !requestID.MatchString(id) {
		return r, errors.New("invalid initialization repair identity")
	}
	if err := db.QueryRow("SELECT bench_id,id,key,digest,previous_id,owner_digest,plan,created,status FROM initialization_repairs WHERE bench_id=? AND id=?", benchID, id).Scan(&r.BenchID, &r.ID, &r.Key, &r.Digest, &r.PreviousID, &r.OwnerDigest, &r.Plan, &created, &r.Status); err != nil {
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

// Start never replaces approved bytes or revives a terminal repair. Supersede
// the prior active record in the same transaction before starting its successor.
func Start(tx *sql.Tx, r Record) error {
	if err := valid(r); err != nil {
		return err
	}
	if r.Status != "active" {
		return errors.New("new initialization repair must be active")
	}
	prior, err := Get(tx, r.BenchID, r.ID)
	if err == nil {
		if prior.Status == "active" && prior.Key == r.Key && prior.Digest == r.Digest && prior.OwnerDigest == r.OwnerDigest && prior.PreviousID == r.PreviousID && prior.CreatedAt.Equal(r.CreatedAt) && bytes.Equal(prior.Plan, r.Plan) {
			return nil
		}
		return errors.New("initialization repair identity already journaled")
	}
	if err != sql.ErrNoRows {
		return err
	}
	if r.PreviousID != "" {
		previous, err := Get(tx, r.BenchID, r.PreviousID)
		if err != nil {
			return err
		}
		if previous.Status != "superseded" || previous.Key != r.Key {
			return errors.New("initialization repair predecessor must be superseded")
		}
	}
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM initialization_repairs").Scan(&count); err != nil {
		return err
	}
	if count >= MaxRecords {
		return errors.New("initialization repair journal budget exhausted")
	}
	_, err = tx.Exec("INSERT INTO initialization_repairs(bench_id,id,key,digest,previous_id,owner_digest,plan,created,status) VALUES(?,?,?,?,?,?,?,?,?)", r.BenchID, r.ID, r.Key, r.Digest, r.PreviousID, r.OwnerDigest, []byte(r.Plan), r.CreatedAt.Format(time.RFC3339Nano), r.Status)
	return err
}

func Complete(tx *sql.Tx, benchID, id, digest string) error {
	return finish(tx, benchID, id, digest, "completed")
}
func Supersede(tx *sql.Tx, benchID, id, digest string) error {
	return finish(tx, benchID, id, digest, "superseded")
}
func finish(tx *sql.Tx, benchID, id, approval, status string) error {
	r, err := Get(tx, benchID, id)
	if err != nil {
		return err
	}
	if r.Digest != approval {
		return errors.New("initialization repair approval changed")
	}
	if r.Status == status {
		return nil
	}
	if r.Status != "active" {
		return errors.New("terminal initialization repair cannot change status")
	}
	_, err = tx.Exec("UPDATE initialization_repairs SET status=? WHERE bench_id=? AND id=? AND status='active'", status, benchID, id)
	return err
}

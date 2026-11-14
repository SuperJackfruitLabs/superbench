// Package history stores run evidence without holding lease capabilities.
package history

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"regexp"
	"time"
)

const MaxRecords = 10000

var ID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Reader interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}
type Record struct {
	ID               string
	Origin           string
	ObservedAt       time.Time
	AdoptionDigest   string
	Key              string
	Holder           string
	StartedAt        time.Time
	ReleasedAt       time.Time
	SealedAt         time.Time
	Checkpoint       string
	ExitCode         *int
	Truncated        bool
	File             *reap.Entry
	Size             int64
	Collection       string
	CollectedAt      time.Time
	CollectedSHA256  string
	CollectionReason string
	Revision         int64
}

func Schema(tx *sql.Tx) error {
	_, e := tx.Exec(`CREATE TABLE IF NOT EXISTS run_history(id TEXT PRIMARY KEY,record TEXT NOT NULL CHECK(json_valid(record)));
 CREATE TABLE IF NOT EXISTS log_collections(digest TEXT PRIMARY KEY,plan TEXT,status TEXT NOT NULL,created TEXT NOT NULL,finished TEXT,count INTEGER NOT NULL,bytes INTEGER NOT NULL,records_count INTEGER NOT NULL,collections_count INTEGER NOT NULL);`)
	return e
}
func Start(tx *sql.Tx, r Record) error {
	if r.Origin == "" {
		r.Origin = "supervised"
	}
	if e := valid(r); e != nil {
		return e
	}
	if !ID.MatchString(r.ID) {
		return errors.New("invalid run history identity")
	}
	var count int
	if e := tx.QueryRow("SELECT count(*) FROM run_history").Scan(&count); e != nil {
		return e
	}
	if count >= MaxRecords {
		return errors.New("run history budget exhausted; review retention")
	}
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO run_history(id,record) VALUES(?,?)", r.ID, string(raw))
	return e
}
func Get(db Reader, id string) (Record, error) {
	var r Record
	if db == nil {
		return r, sql.ErrNoRows
	}
	var raw string
	if e := db.QueryRow("SELECT record FROM run_history WHERE id=?", id).Scan(&raw); e != nil {
		return r, e
	}
	e := json.Unmarshal([]byte(raw), &r)
	if e == nil {
		e = valid(r)
	}
	if e == nil && r.ID != id {
		e = errors.New("invalid history record")
	}
	return r, e
}
func Save(tx *sql.Tx, r Record) error {
	if r.Origin == "" {
		r.Origin = "supervised"
	}
	if e := valid(r); e != nil {
		return e
	}
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	result, e := tx.Exec("UPDATE run_history SET record=? WHERE id=?", string(raw), r.ID)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e == nil && n != 1 {
		return errors.New("history record disappeared")
	}
	return e
}
func List(db Reader, key string, limit int) ([]Record, error) {
	if limit < 1 || limit > MaxRecords {
		return nil, errors.New("invalid history limit")
	}
	out := []Record{}
	if db == nil {
		return out, nil
	}
	rows, e := db.Query("SELECT id,record FROM run_history ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		if e = rows.Scan(&id, &raw); e != nil {
			return nil, e
		}
		var r Record
		if e = json.Unmarshal([]byte(raw), &r); e != nil {
			return nil, e
		}
		if e = valid(r); e != nil {
			return nil, e
		}
		if r.ID != id {
			return nil, errors.New("invalid history record")
		}
		if key == "" || r.Key == key {
			out = append(out, r)
		}
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	// Bounded by admission; callers choose their display limit after filtering.
	sortRecords(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func Released(tx *sql.Tx, id, checkpoint string) error {
	r, e := Get(tx, id)
	if e == sql.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	r.ReleasedAt = time.Now().UTC()
	r.Checkpoint = checkpoint
	return Save(tx, r)
}
func Seal(tx *sql.Tx, id string, file reap.Entry, size int64, exit *int, truncated bool) error {
	r, e := Get(tx, id)
	if e != nil {
		return e
	}
	if r.Collection != "" || !r.CollectedAt.IsZero() {
		return errors.New("history already collecting")
	}
	if r.File != nil {
		if *r.File == file {
			return nil
		}
		return errors.New("sealed log changed")
	}
	r.File = &file
	r.Size = size
	r.SealedAt = time.Now().UTC()
	r.ExitCode = exit
	r.Truncated = truncated
	return Save(tx, r)
}
func Digest(r Record) (string, error) {
	r.Collection = ""
	r.CollectedAt = time.Time{}
	r.CollectedSHA256 = ""
	r.CollectionReason = ""
	raw, e := json.Marshal(r)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (r Record) FileSize() int64 { return r.Size }

func valid(r Record) error {
	if !ID.MatchString(r.ID) || r.Key == "" || len(r.Key) > 80 || len(r.Holder) > 200 || r.Size < 0 {
		return errors.New("invalid history record")
	}
	if r.Origin == "supervised" {
		if r.StartedAt.IsZero() {
			return errors.New("missing run start time")
		}
	} else if r.Origin == "operator-adopted" {
		if r.ObservedAt.IsZero() || len(r.AdoptionDigest) != 64 {
			return errors.New("invalid adopted log provenance")
		}
	} else {
		return errors.New("unknown history origin")
	}
	if r.File != nil {
		if r.File.Path != "run-"+r.ID+".log" || len(r.File.SHA256) != 64 || r.SealedAt.IsZero() {
			return errors.New("invalid sealed log evidence")
		}
	}
	if !r.CollectedAt.IsZero() && (r.ReleasedAt.IsZero() || r.File == nil || len(r.Collection) != 64) {
		return errors.New("invalid collection evidence")
	}
	return nil
}

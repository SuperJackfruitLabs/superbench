package remoteschema

import (
	"database/sql"
	"errors"
	"math"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxAuditRows bounds each audit table. Inserting prunes the oldest rows past
// it: any certificate, but only ended sessions, so an open session is never lost.
const MaxAuditRows = 10000

// A record names who, which bench and lease, and when. It never holds a lease
// token: no field takes one, and callers build records from lease identity.
type CertificateRecord struct {
	Serial                           uint64
	Key, BenchID, LeaseID            string
	Generation                       uint64
	ExecutionID, Holder, Fingerprint string
	IssuedAt, ValidBefore            time.Time
}
type SessionRecord struct {
	ID                                                   int64
	Serial                                               uint64
	Key, BenchID, Fingerprint, Source, Kind, CommandName string
	StartedAt, EndedAt                                   time.Time
	BytesIn, BytesOut                                    uint64
	EndReason                                            string
}

// Reader is a transaction or database handle.
type Reader interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

var errAudit = errors.New("invalid remote access audit record")

// SQLite INTEGER is signed: a value at or past 2^63 would wrap.
func signed(values ...uint64) bool {
	for _, v := range values {
		if v > math.MaxInt64 {
			return false
		}
	}
	return true
}

// counted clamps a byte count, so ending a session never fails on its size.
func counted(v uint64) int64 { return int64(min(v, math.MaxInt64)) }

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
func parse(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func ending(reason string) bool {
	switch reason {
	case "exit", "closed", "fenced", "expired", "revoked", "refused":
		return true
	}
	return false
}

func RecordCertificate(tx *sql.Tx, r CertificateRecord) error {
	if !signed(r.Serial, r.Generation) || r.Key == "" {
		return errAudit
	}
	if _, err := tx.Exec(`INSERT INTO remote_certificates(serial,key,bench_id,lease_id,generation,execution_id,holder,fingerprint,issued_at,valid_before) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		int64(r.Serial), r.Key, r.BenchID, r.LeaseID, int64(r.Generation), r.ExecutionID, r.Holder, r.Fingerprint, stamp(r.IssuedAt), stamp(r.ValidBefore)); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM remote_certificates WHERE rowid IN (SELECT rowid FROM remote_certificates ORDER BY rowid LIMIT max(0,(SELECT count(*) FROM remote_certificates)-?))`, MaxAuditRows)
	return err
}

// StartSession records an open session and returns its row ID.
func StartSession(tx *sql.Tx, r SessionRecord) (int64, error) {
	if !signed(r.Serial) || r.Key == "" || r.BenchID == "" || r.Kind != "shell" && r.Kind != "exec" && r.Kind != "sftp" || CommandName(r.CommandName) != r.CommandName {
		return 0, errAudit
	}
	result, err := tx.Exec(`INSERT INTO remote_sessions(serial,key,bench_id,fingerprint,source,kind,command_name,started_at) VALUES (?,?,?,?,?,?,?,?)`,
		int64(r.Serial), r.Key, r.BenchID, r.Fingerprint, r.Source, r.Kind, r.CommandName, stamp(r.StartedAt))
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(`DELETE FROM remote_sessions WHERE id IN (SELECT id FROM remote_sessions WHERE end_reason!='' ORDER BY id LIMIT max(0,(SELECT count(*) FROM remote_sessions)-?))`, MaxAuditRows)
	return id, err
}

// EndSession ends a session with its byte counts. A session a fence or sweep
// already ended keeps that reason and time and only gains the counts, which
// never go down. An unknown session is an error.
func EndSession(tx *sql.Tx, id int64, at time.Time, in, out uint64, reason string) error {
	if !ending(reason) {
		return errAudit
	}
	result, err := tx.Exec(`UPDATE remote_sessions SET bytes_in=max(bytes_in,?),bytes_out=max(bytes_out,?),ended_at=CASE WHEN end_reason='' THEN ? ELSE ended_at END,end_reason=CASE WHEN end_reason='' THEN ? ELSE end_reason END WHERE id=?`, counted(in), counted(out), stamp(at), reason, id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errors.Join(errors.New("remote session not recorded"), err)
	}
	return nil
}

// EndOpenSessions ends every open session on one bench.
func EndOpenSessions(tx *sql.Tx, benchID string, at time.Time, reason string) error {
	if !ending(reason) {
		return errAudit
	}
	_, err := tx.Exec(`UPDATE remote_sessions SET ended_at=?,end_reason=? WHERE bench_id=? AND end_reason=''`, stamp(at), reason, benchID)
	return err
}

// OpenSessions lists the ids of one bench's open sessions.
func OpenSessions(r Reader, benchID string) ([]int64, error) {
	rows, err := r.Query(`SELECT id FROM remote_sessions WHERE bench_id=? AND end_reason='' ORDER BY id`, benchID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	err = scan(rows, func() error {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	})
	return ids, err
}

// FenceSessions ends a fence: every open session on the bench ends "fenced",
// and so does each session in open (the ones OpenSessions listed before the
// kill) that has ended since. The kill makes a session's own end, "exit" or
// "closed", race the fence's write; the fence's reason wins. A relabelled
// session keeps its end time and byte counts.
func FenceSessions(tx *sql.Tx, benchID string, open []int64, at time.Time) error {
	if err := EndOpenSessions(tx, benchID, at, "fenced"); err != nil {
		return err
	}
	for _, id := range open {
		if _, err := tx.Exec(`UPDATE remote_sessions SET end_reason='fenced' WHERE id=? AND bench_id=?`, id, benchID); err != nil {
			return err
		}
	}
	return nil
}

// Recent returns the newest rows first, for one key or, when key is empty, all.
func Recent(r Reader, key string, limit int) ([]CertificateRecord, []SessionRecord, error) {
	limit = max(1, min(limit, MaxAuditRows))
	rows, err := r.Query(`SELECT serial,key,bench_id,lease_id,generation,execution_id,holder,fingerprint,issued_at,valid_before FROM remote_certificates WHERE ?='' OR key=? ORDER BY rowid DESC LIMIT ?`, key, key, limit)
	if err != nil {
		return nil, nil, err
	}
	var certs []CertificateRecord
	err = scan(rows, func() error {
		var c CertificateRecord
		var serial, generation int64
		var issued, valid string
		if err := rows.Scan(&serial, &c.Key, &c.BenchID, &c.LeaseID, &generation, &c.ExecutionID, &c.Holder, &c.Fingerprint, &issued, &valid); err != nil {
			return err
		}
		c.Serial, c.Generation = uint64(serial), uint64(generation)
		var e1, e2 error
		c.IssuedAt, e1 = parse(issued)
		c.ValidBefore, e2 = parse(valid)
		certs = append(certs, c)
		return errors.Join(e1, e2)
	})
	if err != nil {
		return nil, nil, err
	}
	rows, err = r.Query(`SELECT id,serial,key,bench_id,fingerprint,source,kind,command_name,started_at,ended_at,bytes_in,bytes_out,end_reason FROM remote_sessions WHERE ?='' OR key=? ORDER BY id DESC LIMIT ?`, key, key, limit)
	if err != nil {
		return nil, nil, err
	}
	var sessions []SessionRecord
	err = scan(rows, func() error {
		var s SessionRecord
		var serial, in, out int64
		var started, ended string
		if err := rows.Scan(&s.ID, &serial, &s.Key, &s.BenchID, &s.Fingerprint, &s.Source, &s.Kind, &s.CommandName, &started, &ended, &in, &out, &s.EndReason); err != nil {
			return err
		}
		s.Serial, s.BytesIn, s.BytesOut = uint64(serial), uint64(in), uint64(out)
		var e1, e2 error
		s.StartedAt, e1 = parse(started)
		s.EndedAt, e2 = parse(ended)
		sessions = append(sessions, s)
		return errors.Join(e1, e2)
	})
	if err != nil {
		return nil, nil, err
	}
	return certs, sessions, nil
}

func scan(rows *sql.Rows, each func() error) error {
	var err error
	for err == nil && rows.Next() {
		err = each()
	}
	if err == nil {
		err = rows.Err()
	}
	if e := rows.Close(); err == nil {
		err = e
	}
	return err
}

// CommandName keeps only an exec command's program name: the base name of its
// first field, at most 64 bytes, or "?" when it holds anything unprintable.
// Arguments, which may carry secrets, are never recorded.
func CommandName(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	name := path.Base(fields[0])
	if !utf8.ValidString(name) || strings.IndexFunc(name, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return "?"
	}
	for len(name) > 64 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

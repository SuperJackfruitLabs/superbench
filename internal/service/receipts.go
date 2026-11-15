package service

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	_ "modernc.org/sqlite"
)

var errConflict = errors.New("request id reused with a different operation body")
var errInDoubt = errors.New("request outcome requires reconciliation")
var errInProgress = errors.New("request is running")
var errCapacity = errors.New("request receipt capacity exhausted")

type receipt struct {
	body   []byte
	status int
}
type receipts struct {
	db     *sql.DB
	mu     sync.Mutex
	active map[string]bool
}

func openReceipts(state string) (*receipts, error) {
	path := filepath.Join(state, "service.db")
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	stat, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	if !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("receipt database must be a private regular file")
	}
	if e = f.Close(); e != nil {
		return nil, e
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	var version int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
		db.Close()
		return nil, e
	}
	if version != 0 && version != 1 {
		db.Close()
		return nil, errors.New("unsupported receipt schema")
	}
	if version == 0 {
		tx, e := db.Begin()
		if e != nil {
			db.Close()
			return nil, e
		}
		if _, e = tx.Exec(`CREATE TABLE receipts (owner INTEGER NOT NULL, id TEXT NOT NULL, digest TEXT NOT NULL, state TEXT NOT NULL, body BLOB, status INTEGER, PRIMARY KEY(owner,id))`); e != nil {
			tx.Rollback()
			db.Close()
			return nil, e
		}
		if _, e = tx.Exec("PRAGMA user_version=1"); e != nil {
			tx.Rollback()
			db.Close()
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			db.Close()
			return nil, e
		}
	}

	if _, e = db.Exec("UPDATE receipts SET state='interrupted' WHERE state='running'"); e != nil {
		db.Close()
		return nil, e
	}
	return &receipts{db: db, active: map[string]bool{}}, nil
}

// lookup does not reserve an identity: completed responses precede mutable
// ownership preflight, while a missing identity still needs admission checks.
func (r *receipts) lookup(uid uint32, id, digest string) (*receipt, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lookupLocked(uid, id, digest)
}
func (r *receipts) lookupLocked(uid uint32, id, digest string) (*receipt, bool, error) {
	key := receiptKey(uid, id)
	var prior, state string
	var body []byte
	var status sql.NullInt64
	err := r.db.QueryRow("SELECT digest,state,body,status FROM receipts WHERE owner=? AND id=?", uid, id).Scan(&prior, &state, &body, &status)
	if err == nil {
		if prior != digest {
			return nil, true, errConflict
		}
		if state == "done" && status.Valid {
			return &receipt{body: body, status: int(status.Int64)}, true, nil
		}
		if r.active[key] {
			return nil, true, errInProgress
		}
		return nil, true, errInDoubt
	}
	if err != sql.ErrNoRows {
		return nil, true, err
	}
	// Missing durable authority must never permit a second in-process dispatch.
	if _, retained := r.active[key]; retained {
		return nil, true, errInDoubt
	}
	return nil, false, nil
}
func (r *receipts) claim(uid uint32, id, digest string) (*receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cached, found, err := r.lookupLocked(uid, id, digest); found || err != nil {
		return cached, err
	}
	key := receiptKey(uid, id)
	var e error
	var count, bytes, pending int64
	if e = r.db.QueryRow("SELECT count(*),coalesce(sum(length(body)),0),coalesce(sum(CASE WHEN state='running' THEN 1 ELSE 0 END),0) FROM receipts").Scan(&count, &bytes, &pending); e != nil {
		return nil, e
	}
	if count >= 10000 || bytes+(pending+1)*(64<<10) > 64<<20 {
		return nil, errCapacity
	}
	if _, e = r.db.Exec("INSERT INTO receipts(owner,id,digest,state) VALUES (?,?,?,'running')", uid, id, digest); e != nil {
		return nil, e
	}
	r.active[key] = true
	return nil, nil
}
func (r *receipts) finish(uid uint32, id string, body []byte, status int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := receiptKey(uid, id)
	// Retain an in-process tombstone on failed persistence, including a missing
	// row. A failure must never reopen the same identity for dispatch.
	defer func() {
		if r.active[key] {
			r.active[key] = false
		}
	}()
	if len(body) > 64<<10 {
		return errCapacity
	}
	if !r.active[receiptKey(uid, id)] {
		return errInDoubt
	}
	result, e := r.db.Exec("UPDATE receipts SET state='done',body=?,status=? WHERE owner=? AND id=? AND state='running'", body, status, uid, id)
	if e != nil {
		return e
	}
	changed, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if changed != 1 {
		return errInDoubt
	}
	delete(r.active, key)
	return nil
}

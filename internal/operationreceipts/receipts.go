package operationreceipts

import (
	"database/sql"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	_ "modernc.org/sqlite"
)

const receiptSchemaVersion = 6

var errConflict = errors.New("request id reused with a different operation body")
var errInDoubt = errors.New("request outcome requires reconciliation")
var errInProgress = errors.New("request is running")
var errCancelRequested = errors.New("operation cancellation requested")
var errCapacity = errors.New("request receipt capacity exhausted")

type receipt struct {
	body   []byte
	status int
}
type receipts struct {
	db          *sql.DB
	mu          sync.Mutex
	active      map[string]bool
	controllers map[string]func()
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
	if version < 0 || version > receiptSchemaVersion {
		db.Close()
		return nil, errors.New("unsupported receipt schema")
	}
	if version < 6 {
		var future int
		if e = db.QueryRow(`SELECT count(*) FROM pragma_table_info('operation_intents') WHERE name='core_finalized'`).Scan(&future); e != nil || future != 0 {
			db.Close()
			if e != nil {
				return nil, e
			}
			return nil, errInDoubt
		}
	}
	if version < 5 {
		var future int
		if e = db.QueryRow(`SELECT count(*) FROM pragma_table_info('operation_intents') WHERE name='helper_tracking'`).Scan(&future); e != nil || future != 0 {
			db.Close()
			if e != nil {
				return nil, e
			}
			return nil, errInDoubt
		}
	}
	if version < 4 {
		var future int
		if e = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='operation_helpers'`).Scan(&future); e != nil || future != 0 {
			db.Close()
			if e != nil {
				return nil, e
			}
			return nil, errInDoubt
		}
	}
	if version < 2 {
		tx, e := db.Begin()
		if e != nil {
			db.Close()
			return nil, e
		}
		statement := `ALTER TABLE receipts ADD COLUMN cancel_state TEXT NOT NULL DEFAULT ''`
		if version == 0 {
			statement = `CREATE TABLE receipts (owner INTEGER NOT NULL, id TEXT NOT NULL, digest TEXT NOT NULL, state TEXT NOT NULL, body BLOB, status INTEGER, cancel_state TEXT NOT NULL DEFAULT '', PRIMARY KEY(owner,id))`
		}
		if _, e = tx.Exec(statement); e != nil {
			tx.Rollback()
			db.Close()
			return nil, e
		}
		if _, e = tx.Exec(`CREATE TABLE operation_cancels (owner INTEGER NOT NULL, id TEXT NOT NULL, digest TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('cancel_requested','cancelled')), PRIMARY KEY(owner,id))`); e != nil {
			tx.Rollback()
			db.Close()
			return nil, e
		}
		if _, e = tx.Exec("PRAGMA user_version=2"); e != nil {
			tx.Rollback()
			db.Close()
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			db.Close()
			return nil, e
		}
	}

	if version < 3 {
		tx, e := db.Begin()
		if e != nil {
			db.Close()
			return nil, e
		}
		if _, e = tx.Exec(`CREATE TABLE operation_intents(owner INTEGER NOT NULL,id TEXT NOT NULL,digest TEXT NOT NULL,key TEXT NOT NULL,operation TEXT NOT NULL,epoch TEXT NOT NULL,PRIMARY KEY(owner,id));PRAGMA user_version=3`); e != nil {
			tx.Rollback()
			db.Close()
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			db.Close()
			return nil, e
		}
	}
	if version < 4 {
		tx, err := db.Begin()
		if err != nil {
			db.Close()
			return nil, err
		}
		if _, err = tx.Exec(`CREATE TABLE operation_helpers(owner INTEGER NOT NULL,id TEXT NOT NULL,kind TEXT NOT NULL,key TEXT NOT NULL,invocation TEXT NOT NULL,PRIMARY KEY(owner,id,kind,key));PRAGMA user_version=4`); err != nil {
			tx.Rollback()
			db.Close()
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version < 5 {
		tx, err := db.Begin()
		if err != nil {
			db.Close()
			return nil, err
		}
		if _, err = tx.Exec(`ALTER TABLE operation_intents ADD COLUMN helper_tracking INTEGER NOT NULL DEFAULT 0;PRAGMA user_version=5`); err != nil {
			tx.Rollback()
			db.Close()
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version < 6 {
		tx, err := db.Begin()
		if err != nil {
			db.Close()
			return nil, err
		}
		if _, err = tx.Exec(`ALTER TABLE operation_intents ADD COLUMN core_finalized INTEGER NOT NULL DEFAULT 0;PRAGMA user_version=6`); err != nil {
			tx.Rollback()
			db.Close()
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	ledger := &receipts{db: db, active: map[string]bool{}}
	if e = ledger.validateHistory(); e != nil {
		db.Close()
		return nil, e
	}
	if e = activeIndexes(db); e != nil {
		db.Close()
		return nil, e
	}
	if _, e = db.Exec("UPDATE receipts SET state='interrupted' WHERE state='running'"); e != nil {
		db.Close()
		return nil, e
	}
	return ledger, nil
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
	var prior, state, cancelled string
	var body []byte
	var status sql.NullInt64
	err := r.db.QueryRow("SELECT digest,state,cancel_state,body,status FROM receipts WHERE owner=? AND id=?", uid, id).Scan(&prior, &state, &cancelled, &body, &status)
	if err == nil {
		if prior != digest {
			return nil, true, errConflict
		}
		if err = r.validateReceipt(storedReceipt{target: operationcontrol.Target{UID: uid, RequestID: id, Digest: prior}, state: state, barrier: cancelled, body: body, status: status}); err != nil {
			return nil, true, err
		}
		if state == "done" && status.Valid {
			return &receipt{body: body, status: int(status.Int64)}, true, nil
		}
		if cancelled != "" {
			return nil, true, errCancelRequested
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
	if _, err := r.operationIntent(operationcontrol.Target{UID: uid, RequestID: id, Digest: digest}); err == nil {
		return nil, true, errInDoubt
	} else if err != sql.ErrNoRows {
		return nil, true, err
	}
	if retained, err := r.retainedHelpers(uid, id); err != nil || retained {
		if err != nil {
			return nil, true, err
		}
		return nil, true, errInDoubt
	}
	if err := cancelBarrier(r.db, uid, id, digest); err != nil {
		return nil, true, err
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
	tx, e := r.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = cancelBarrier(tx, uid, id, digest); e != nil {
		return nil, e
	}
	if e = requireReceiptCapacity(tx); e != nil {
		return nil, e
	}
	result, e := tx.Exec("INSERT INTO receipts(owner,id,digest,state) VALUES (?,?,?,'running')", uid, id, digest)
	if e = oneInserted(result, e); e != nil {
		return nil, e
	}
	var storedDigest, state, barrier string
	var body []byte
	var status sql.NullInt64
	if e = tx.QueryRow(`SELECT digest,state,cancel_state,body,status FROM receipts WHERE owner=? AND id=?`, uid, id).Scan(&storedDigest, &state, &barrier, &body, &status); e != nil {
		return nil, e
	}
	if storedDigest != digest || state != "running" || barrier != "" || body != nil || status.Valid {
		return nil, errInDoubt
	}
	if e = tx.Commit(); e != nil {
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
	if e := admittedCancelBarrier(r.db, uid, id); e != nil {
		return e
	}
	result, e := r.db.Exec("UPDATE receipts SET state='done',body=?,status=? WHERE owner=? AND id=? AND state='running' AND cancel_state=''", body, status, uid, id)
	if e != nil {
		return e
	}
	changed, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if changed != 1 {
		if e := admittedCancelBarrier(r.db, uid, id); e != nil {
			return e
		}
		return errInDoubt
	}
	delete(r.active, key)
	return nil
}

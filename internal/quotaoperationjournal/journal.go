package quotaoperationjournal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

var ErrAuthority = errors.New("quota operation journal authority changed")
var ErrCapacity = errors.New("quota operation pending capacity exhausted")

const MaxPending = 10000

const schema = `CREATE TABLE identities(id TEXT PRIMARY KEY NOT NULL, identity BLOB NOT NULL, denied INTEGER NOT NULL CHECK(denied IN (0,1))) WITHOUT ROWID;
CREATE TABLE pending(name TEXT PRIMARY KEY NOT NULL, id TEXT NOT NULL, identity BLOB NOT NULL) WITHOUT ROWID;
CREATE INDEX pending_identity ON pending(id);
PRAGMA user_version=1;`

var pendingPattern = regexp.MustCompile(`^pending-[a-f0-9]{64}-[a-f0-9]{32}-[0-9]{1,32}$`)

// Journal is usable only during its Store callback. No method returns a lease
// capability or infers that a state0 identity has completed its operation.
type Journal struct {
	db       *sql.DB
	check    func() error
	active   bool
	ctx      context.Context
	identity *quotaoperation.Identity
	durable  func() error
}

func rawIdentity(i quotaoperation.Identity) ([]byte, error) {
	if !i.Valid() {
		return nil, ErrAuthority
	}
	return json.Marshal(i)
}

func (j *Journal) ready() error {
	if j == nil || !j.active || j.check == nil {
		return ErrAuthority
	}
	if err := j.check(); err != nil {
		return err
	}
	return validateSchema(j.db)
}

// Lookup checks every immutable field, including any retained pending identity.
// found=false never grants authority to choose a different identity or epoch.
func (j *Journal) Lookup(i quotaoperation.Identity) (found, denied bool, err error) {
	if j == nil || j.identity == nil || i != *j.identity {
		return false, false, ErrAuthority
	}
	raw, err := rawIdentity(i)
	if err != nil {
		return false, false, err
	}
	if err = j.ready(); err != nil {
		return false, false, err
	}
	var conflicting int
	if err = j.db.QueryRow(`SELECT count(*) FROM pending WHERE id=? AND identity<>?`, i.OperationID, raw).Scan(&conflicting); err != nil || conflicting != 0 {
		if err == nil {
			err = ErrAuthority
		}
		return false, false, err
	}
	var saved []byte
	var state int
	err = j.db.QueryRow(`SELECT identity,denied FROM identities WHERE id=?`, i.OperationID).Scan(&saved, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, j.ready()
	}
	if err != nil {
		return false, false, err
	}
	if !bytes.Equal(saved, raw) || state != 0 && state != 1 {
		return false, false, ErrAuthority
	}
	return true, state == 1, j.ready()
}

// Reserve commits and reads back the exact identity before the caller may run
// effects. An exact existing denied identity is never changed back to admitting.
func (j *Journal) Reserve(i quotaoperation.Identity) error {
	found, denied, err := j.Lookup(i)
	if err != nil {
		return err
	}
	if denied {
		return quotaoperation.ErrCancelled
	}
	if found {
		// A prior writer may have stopped while its visible commit remained in
		// the surviving kernel cache. Re-establish both pinned files' durability
		// before an exact retry is allowed to invoke the fixed effect.
		if j.durable == nil {
			return ErrAuthority
		}
		if err = j.durable(); err != nil {
			return err
		}
		return j.ready()
	}
	raw, _ := rawIdentity(i)
	return j.write(func(tx *sql.Tx) error {
		r, err := tx.Exec(`INSERT INTO identities(id,identity,denied) VALUES(?,?,0)`, i.OperationID, raw)
		if err != nil {
			return err
		}
		if n, e := r.RowsAffected(); e != nil || n != 1 {
			return ErrAuthority
		}
		var saved []byte
		var state int
		if err = tx.QueryRow(`SELECT identity,denied FROM identities WHERE id=?`, i.OperationID).Scan(&saved, &state); err != nil {
			return err
		}
		if !bytes.Equal(saved, raw) || state != 0 {
			return ErrAuthority
		}
		return nil
	})
}

// Deny is monotonic and does not allocate another permanent identity slot when
// the identity is already known. Proof is the caller's responsibility after this
// method and its final authority/durability checks have succeeded.
func (j *Journal) Deny(i quotaoperation.Identity) error {
	_, denied, err := j.Lookup(i)
	if err != nil {
		return err
	}
	if !denied {
		if err = j.Reserve(i); err != nil {
			return err
		}
	}
	raw, _ := rawIdentity(i)
	return j.write(func(tx *sql.Tx) error {
		r, e := tx.Exec(`UPDATE identities SET denied=1 WHERE id=? AND identity=?`, i.OperationID, raw)
		if e != nil {
			return e
		}
		if n, e := r.RowsAffected(); e != nil || n != 1 {
			return ErrAuthority
		}
		var state int
		if e = tx.QueryRow(`SELECT denied FROM identities WHERE id=? AND identity=?`, i.OperationID, raw).Scan(&state); e != nil {
			return e
		}
		if state != 1 {
			return ErrAuthority
		}
		return nil
	})
}

// RetainPending is only for a bounded legacy-import compartment. New live SQL
// admissions have no per-invocation pending file or growing pending-row history.
// It records a validated complete legacy reservation without making
// it a canonical admission. No method adopts, expires or deletes pending rows.
func (j *Journal) RetainPending(name string, i quotaoperation.Identity) error {
	raw, err := rawIdentity(i)
	if err != nil {
		return err
	}
	if !pendingPattern.MatchString(name) || name[8:72] != i.OperationID || name[73:105] != i.Epoch {
		return ErrAuthority
	}
	if _, _, err = j.Lookup(i); err != nil {
		return err
	}
	return j.write(func(tx *sql.Tx) error {
		var id string
		var saved []byte
		e := tx.QueryRow(`SELECT id,identity FROM pending WHERE name=?`, name).Scan(&id, &saved)
		if e == nil {
			if id != i.OperationID || !bytes.Equal(saved, raw) {
				return ErrAuthority
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var count int
		if e = tx.QueryRow(`SELECT count(*) FROM pending`).Scan(&count); e != nil {
			return e
		}
		if count >= MaxPending {
			return ErrCapacity
		}
		r, e := tx.Exec(`INSERT INTO pending(name,id,identity) VALUES(?,?,?)`, name, i.OperationID, raw)
		if e != nil {
			return e
		}
		if n, e := r.RowsAffected(); e != nil || n != 1 {
			return ErrAuthority
		}
		if e = tx.QueryRow(`SELECT id,identity FROM pending WHERE name=?`, name).Scan(&id, &saved); e != nil {
			return e
		}
		if id != i.OperationID || !bytes.Equal(saved, raw) {
			return ErrAuthority
		}
		return nil
	})
}

func (j *Journal) write(fn func(*sql.Tx) error) error {
	if err := j.ready(); err != nil {
		return err
	}
	tx, err := j.db.BeginTx(j.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	if err = j.check(); err != nil {
		return err
	}
	if err = validateSchema(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return j.ready()
}

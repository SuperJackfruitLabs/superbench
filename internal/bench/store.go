// Package bench implements a local, cooperative single-writer prototype.
package bench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statepath"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"os"
	"time"
)

type Lease = model.Lease
type Bench = model.Bench
type Store struct {
	State, Data        string
	CgroupRoot         string
	MirrorTimeout      time.Duration
	controls           context.Context
	reapAfterDelete    func() error
	collectAfterDelete func() error
	executor           executionBackend
	spaceProbe         func(string) (Space, error)
	quotaApply         func(HostConfig, string, string, uint64) (hoststorage.Assignment, error)
}
type database = benchstate.Database

func (s Store) validate() error {
	if s.MirrorTimeout < 0 || s.MirrorTimeout > time.Hour {
		return errors.New("mirror timeout must be between zero (default) and one hour")
	}
	return statepath.Validate(s.State, s.Data)
}

// The host-wide OS lock also serializes mirror/worktree operations. SQLite
// transactions make the authoritative record update atomic independently.
func (s Store) transaction(fn func(database) error) error {
	return s.transactionSQL(func(records database, _ *sql.Tx) error { return fn(records) })
}
func (s Store) transactionSQL(fn func(database, *sql.Tx) error) error {
	return s.session(func(session *statestore.Session) error { return s.update(session, fn) })
}

// A session retains the host lock across sequential durable state transitions.
// It must not call a public mutation recursively: use update on this session.
func (s Store) session(fn func(*statestore.Session) error) error {
	if err := s.validate(); err != nil {
		return err
	}
	return statestore.Within(s.State, gitcontrol.Recover, fn)
}

func (s Store) update(session *statestore.Session, fn func(database, *sql.Tx) error) error {
	return session.Update(func(tx *sql.Tx) error {
		if err := s.migrate(tx); err != nil {
			return err
		}
		records, err := loadRecords(tx)
		if err != nil {
			return err
		}
		before := benchstate.Capture(records)
		if err = fn(records, tx); err != nil {
			return err
		}
		if err = benchstate.ValidateTransition(before, records); err != nil {
			return err
		}
		if err = validRecords(records); err != nil {
			return err
		}
		for key, b := range records {
			raw, e := json.Marshal(b)
			if e != nil {
				return e
			}
			if _, e = tx.Exec("INSERT INTO benches(key, record) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET record=excluded.record WHERE record != excluded.record", key, string(raw)); e != nil {
				return e
			}
		}
		return nil
	})
}

// Status does not create directories, lock files, databases or journal files.
func (s Store) read(fn func(database) error) error {
	return s.readSQL(func(records database, _ reader) error { return fn(records) })
}
func (s Store) readSQL(fn func(database, reader) error) error {
	return s.readSQLMode(fn, false)
}

// readKinds permits historical scalar records for pre-migration compatibility checks.
func (s Store) readKinds(fn func(database) error) error {
	return s.readSQLMode(func(records database, _ reader) error { return fn(records) }, true)
}

func (s Store) readSQLMode(fn func(database, reader) error, kinds bool) error {
	if err := s.validate(); err != nil {
		return err
	}
	return statestore.Read(s.State, func(db *sql.DB) error {
		if db == nil {
			records, err := s.legacy()
			if err != nil {
				return err
			}
			return fn(records, nil)
		}
		var records database
		var err error
		if kinds {
			records, err = benchstate.LoadKinds(db, s.State)
		} else if err = checkVersion(db); err == nil {
			records, err = loadRecords(db)
		}
		if err != nil {
			return err
		}
		return fn(records, db)
	})
}

func atomicWrite(dirPath, target string, raw []byte) error {
	tmp, err := os.CreateTemp(dirPath, "state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), target); err != nil {
		return err
	}
	dir, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

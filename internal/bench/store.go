// Package bench implements a local, cooperative single-writer prototype.
package bench

import (
	"database/sql"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Lease = model.Lease
type Bench = model.Bench
type Store struct {
	State, Data        string
	CgroupRoot         string
	MirrorTimeout      time.Duration
	reapAfterDelete    func() error
	collectAfterDelete func() error
	executor           executionBackend
	spaceProbe         func(string) (Space, error)
	quotaApply         func(HostConfig, string, string) (hoststorage.Assignment, error)
}
type database map[string]*Bench

func (s Store) validate() error {
	if s.MirrorTimeout < 0 || s.MirrorTimeout > time.Hour {
		return errors.New("mirror timeout must be between zero (default) and one hour")
	}
	for _, p := range []string{s.State, s.Data} {
		if !filepath.IsAbs(p) {
			return errors.New("state and data paths must be absolute")
		}
	}
	// Canonicalize existing ancestors as well as existing directories to reject symlink overlap.
	canonical := func(p string) (string, error) {
		var tail []string
		for {
			resolved, err := filepath.EvalSymlinks(p)
			if err == nil {
				for i := len(tail) - 1; i >= 0; i-- {
					resolved = filepath.Join(resolved, tail[i])
				}
				return resolved, nil
			}
			if !os.IsNotExist(err) {
				return "", err
			}
			tail = append(tail, filepath.Base(p))
			p = filepath.Dir(p)
		}
	}
	state, err := canonical(s.State)
	if err != nil {
		return err
	}
	data, err := canonical(s.Data)
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{state, data}, {data, state}} {
		rel, e := filepath.Rel(pair[0], pair[1])
		if e != nil {
			return e
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return errors.New("state and data directories must be separate, non-nested paths")
		}
	}
	return nil
}

// The host-wide OS lock also serializes mirror/worktree operations. SQLite
// transactions make the authoritative record update atomic independently.
func (s Store) transaction(fn func(database) error) error {
	return s.transactionSQL(func(records database, _ *sql.Tx) error { return fn(records) })
}
func (s Store) transactionSQL(fn func(database, *sql.Tx) error) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.State, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.State, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := s.openSQL(false)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.migrate(tx); err != nil {
		return err
	}
	records, err := loadRecords(tx)
	if err != nil {
		return err
	}
	if err = fn(records, tx); err != nil {
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
	return tx.Commit()
}

// Status does not create directories, lock files, databases or journal files.
func (s Store) read(fn func(database) error) error {
	return s.readSQL(func(records database, _ reader) error { return fn(records) })
}
func (s Store) readSQL(fn func(database, reader) error) error {
	if err := s.validate(); err != nil {
		return err
	}
	lock, err := os.Open(filepath.Join(s.State, "lock"))
	if err == nil {
		defer lock.Close()
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_SH); err != nil {
			return err
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err = os.Stat(filepath.Join(s.State, "bench.db")); os.IsNotExist(err) {
		records, e := s.legacy()
		if e != nil {
			return e
		}
		return fn(records, nil)
	} else if err != nil {
		return err
	}
	db, err := s.openSQL(true)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = checkVersion(db); err != nil {
		return err
	}
	records, err := loadRecords(db)
	if err != nil {
		return err
	}
	return fn(records, db)
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

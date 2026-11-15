package statestore

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
)

var ErrSessionClosed = errors.New("state session is closed")
var ErrUpdateActive = errors.New("state session update is already active")
var errNilCallback = errors.New("state session callback is required")

// Session permits sequential durable transactions while its host-wide OS lock
// remains held. It is valid only inside Within's callback. Reentrant/concurrent
// updates reject immediately; they never wait for their own transaction.
type Session struct {
	db     *sql.DB
	update sync.Mutex
	closed atomic.Bool
}

// Within preserves the fixed state/lock lock across all commits in fn. before
// is optional and runs once under that lock, with the resolved state directory,
// before opening SQLite. No schema, migration, or lifecycle policy lives here.
func Within(state string, before func(string) error, fn func(*Session) error) (err error) {
	if fn == nil {
		return errNilCallback
	}
	if err = os.MkdirAll(state, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(state, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	canonical, err := filepath.EvalSymlinks(state)
	if err != nil {
		return err
	}
	if before != nil {
		if err = before(canonical); err != nil {
			return err
		}
	}
	db, err := Open(filepath.Join(state, "bench.db"), false)
	if err != nil {
		return err
	}
	session := &Session{db: db}
	defer func() {
		session.closed.Store(true)
		// An in-flight update must finish before closing SQLite or unlocking the
		// host, even if a caller mistakenly let its goroutine escape fn.
		session.update.Lock()
		closeErr := db.Close()
		session.update.Unlock()
		if err == nil {
			err = closeErr
		}
	}()
	return fn(session)
}

func (s *Session) Update(fn func(*sql.Tx) error) error {
	if s == nil || s.closed.Load() {
		return ErrSessionClosed
	}
	if fn == nil {
		return errNilCallback
	}
	if !s.update.TryLock() {
		return ErrUpdateActive
	}
	defer s.update.Unlock()
	if s.closed.Load() || s.db == nil {
		return ErrSessionClosed
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

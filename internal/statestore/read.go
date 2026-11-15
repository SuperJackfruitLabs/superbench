package statestore

import (
	"database/sql"
	"os"
	"path/filepath"
	"syscall"
)

// Read holds an existing shared host lock and opens an existing database read-only.
// It never creates state paths, lock files or databases. A nil database denotes
// absent SQL state; the caller may inspect its historical import source.
func Read(state string, fn func(*sql.DB) error) error {
	lock, err := os.Open(filepath.Join(state, "lock"))
	if err == nil {
		defer lock.Close()
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_SH); err != nil {
			return err
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	} else if !os.IsNotExist(err) {
		return err
	}
	path := filepath.Join(state, "bench.db")
	if _, err = os.Stat(path); os.IsNotExist(err) {
		return fn(nil)
	} else if err != nil {
		return err
	}
	db, err := Open(path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(db)
}

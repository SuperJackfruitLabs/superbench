// Package statestore opens the private SQLite files used by lifecycle stores.
package statestore

import (
	"database/sql"
	"net/url"
	"os"

	_ "modernc.org/sqlite"
)

// Open preserves explicit read-only/read-write modes, one connection, durable
// synchronous writes and a bounded busy wait. The caller creates and protects
// the parent state directory and owns locking, transactions and migrations.
func Open(path string, readOnly bool) (*sql.DB, error) {
	if !readOnly {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	if readOnly {
		q.Set("mode", "ro")
	} else {
		q.Set("mode", "rw")
	}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

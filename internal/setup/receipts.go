package setup

import (
	"database/sql"
	"errors"
	"time"
)

type Reader interface {
	QueryRow(string, ...any) *sql.Row
}

func Schema(tx *sql.Tx) error {
	_, e := tx.Exec(`CREATE TABLE IF NOT EXISTS setup_receipts(
bench TEXT PRIMARY KEY, digest TEXT NOT NULL, run_id TEXT NOT NULL, completed TEXT NOT NULL)`)
	return e
}

func Completed(db Reader, key, digest string) (bool, error) {
	var stored string
	e := db.QueryRow("SELECT digest FROM setup_receipts WHERE bench=?", key).Scan(&stored)
	if e == sql.ErrNoRows {
		return false, nil
	}
	return stored == digest, e
}

// A different setup attempt can alter the old preparation even when it fails.
// Invalidate in the lease-acquisition transaction, before launching any hook.
func Invalidate(tx *sql.Tx, key string) error {
	_, e := tx.Exec("DELETE FROM setup_receipts WHERE bench=?", key)
	return e
}

func Admit(db Reader, key string) error {
	var count int
	e := db.QueryRow("SELECT count(*) FROM setup_receipts WHERE bench != ?", key).Scan(&count)
	if e != nil {
		return e
	}
	if count >= 10000 {
		return errors.New("setup receipt budget exhausted")
	}
	return nil
}

func Record(tx *sql.Tx, key, digest, runID string) error {
	if e := Admit(tx, key); e != nil {
		return e
	}
	_, e := tx.Exec(`INSERT INTO setup_receipts(bench,digest,run_id,completed) VALUES(?,?,?,?)
ON CONFLICT(bench) DO UPDATE SET digest=excluded.digest,run_id=excluded.run_id,completed=excluded.completed`, key, digest, runID, time.Now().UTC().Format(time.RFC3339Nano))
	return e
}

// Package handoffjournal retains exclusive cross-host offers and transfers in
// the core transaction. It does not inspect hosts, fence writers or run Git.
// These tables require a future core schema migration before production use.
package handoffjournal

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
)

const MaxRecords = 10000
const MaxApproval = 16 << 20

var identity = regexp.MustCompile(`^[a-f0-9]{32}$`)
var ErrAuthority = errors.New("cross-host journal authority differs")

// Schema is called only by a versioned core migration. A lost identity in an
// existing table is refused; reopening must never create a new state identity.
func Schema(tx *sql.Tx) error {
	var existed int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workspace_handoff_identity'`).Scan(&existed); err != nil {
		return err
	}
	var companions int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('workspace_handoff_offers','workspace_handoff_exports','workspace_handoff_imports')`).Scan(&companions); err != nil {
		return err
	}
	if existed == 0 && companions != 0 || existed == 1 && companions != 3 {
		return ErrAuthority
	}
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS workspace_handoff_identity(id TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS workspace_handoff_offers (
 creation_id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL UNIQUE,key TEXT NOT NULL UNIQUE,body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS workspace_handoff_exports (
 transfer_id TEXT PRIMARY KEY,source_id TEXT NOT NULL UNIQUE,source_key TEXT NOT NULL UNIQUE,
 artifact BLOB NOT NULL,approval BLOB NOT NULL,phase TEXT NOT NULL CHECK(phase IN ('started','relinquished')));
CREATE TABLE IF NOT EXISTS workspace_handoff_imports (
 creation_id TEXT PRIMARY KEY,source_host TEXT NOT NULL,source_state TEXT NOT NULL,source_id TEXT NOT NULL,
 transfer_id TEXT NOT NULL,artifact BLOB NOT NULL,phase TEXT NOT NULL CHECK(phase IN ('accepted','completed')),
 UNIQUE(source_host,source_state,source_id),UNIQUE(source_host,source_state,transfer_id));`)
	if err != nil {
		return err
	}
	if existed == 0 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		result, err := tx.Exec(`INSERT INTO workspace_handoff_identity(id) VALUES(?)`, hex.EncodeToString(nonce[:]))
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return ErrAuthority
		}
	}
	_, err = StateID(tx)
	return err
}

type Reader interface{ QueryRow(string, ...any) *sql.Row }

func StateID(db Reader) (string, error) {
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM workspace_handoff_identity`).Scan(&count); err != nil {
		return "", err
	}
	if count != 1 {
		return "", ErrAuthority
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM workspace_handoff_identity`).Scan(&id); err != nil {
		return "", err
	}
	if !identity.MatchString(id) {
		return "", ErrAuthority
	}
	return id, nil
}

func budget(tx *sql.Tx, table string) error {
	// The table is selected only from these private fixed call sites.
	switch table {
	case "workspace_handoff_offers", "workspace_handoff_exports", "workspace_handoff_imports":
	default:
		return ErrAuthority
	}
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
		return err
	}
	if count >= MaxRecords {
		return errors.New("cross-host journal capacity exhausted")
	}
	return nil
}

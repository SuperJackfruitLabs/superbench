// Package remoteschema owns the state schema for remote access: lease identity
// and the certificate and session audit tables. No table stores a lease token.
package remoteschema

import (
	"database/sql"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

const SchemaVersion = 31

// ErrAuthority refuses state below schema 31 that already holds remote access
// tables: an old schema never adopts certificate or session authority.
var ErrAuthority = errors.New("remote access tables require state schema 31")

// An open session has an empty ended_at and end_reason.
const tables = `CREATE TABLE remote_certificates(serial INTEGER NOT NULL UNIQUE,key TEXT NOT NULL,bench_id TEXT NOT NULL,lease_id TEXT NOT NULL,generation INTEGER NOT NULL,execution_id TEXT NOT NULL,holder TEXT NOT NULL,fingerprint TEXT NOT NULL,issued_at TEXT NOT NULL,valid_before TEXT NOT NULL);
CREATE INDEX remote_certificates_key ON remote_certificates(key);
CREATE TABLE remote_sessions(id INTEGER PRIMARY KEY,serial INTEGER NOT NULL,key TEXT NOT NULL,bench_id TEXT NOT NULL,fingerprint TEXT NOT NULL,source TEXT NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('shell','exec','sftp')),command_name TEXT NOT NULL,started_at TEXT NOT NULL,ended_at TEXT NOT NULL DEFAULT '',bytes_in INTEGER NOT NULL DEFAULT 0,bytes_out INTEGER NOT NULL DEFAULT 0,end_reason TEXT NOT NULL DEFAULT '' CHECK(end_reason IN ('','exit','closed','fenced','expired','revoked','refused')));
CREATE INDEX remote_sessions_key ON remote_sessions(key);
CREATE INDEX remote_sessions_open ON remote_sessions(bench_id,end_reason);`

// Upgrade31 moves schema 30 state to 31 inside the caller's transaction. Each
// lease without an ID gets its own ID at generation 1; other records keep their
// bytes. Migrate calls it only below 31, so existing audit tables are refused.
func Upgrade31(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('remote_certificates','remote_sessions')`).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return ErrAuthority
	}
	rows, err := tx.Query(`SELECT key FROM benches WHERE json_type(record,'$.Lease')='object' AND coalesce(json_extract(record,'$.Lease.ID'),'')=''`)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	if e := rows.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	for _, key := range keys {
		id, err := model.Nonce()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE benches SET record=json_set(record,'$.Lease.ID',?,'$.Lease.Generation',1) WHERE key=?`, id, key); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(tables); err != nil {
		return err
	}
	_, err = tx.Exec("PRAGMA user_version=31")
	return err
}

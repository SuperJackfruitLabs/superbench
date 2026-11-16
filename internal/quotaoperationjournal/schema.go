package quotaoperationjournal

import "database/sql"

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func validateSchema(db queryer) error {
	var sync, busy int
	var journal string
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&sync); err != nil {
		return err
	}
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		return err
	}
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		return err
	}
	if sync != 2 || busy != 5000 || journal != "persist" {
		return ErrAuthority
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version != 1 {
		return ErrAuthority
	}
	rows, err := db.Query(`SELECT type,name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := map[string]string{"identities": `CREATE TABLE identities(id TEXT PRIMARY KEY NOT NULL, identity BLOB NOT NULL, denied INTEGER NOT NULL CHECK(denied IN (0,1))) WITHOUT ROWID`, "pending": `CREATE TABLE pending(name TEXT PRIMARY KEY NOT NULL, id TEXT NOT NULL, identity BLOB NOT NULL) WITHOUT ROWID`, "pending_identity": `CREATE INDEX pending_identity ON pending(id)`}
	n := 0
	for rows.Next() {
		var typ, name, statement string
		if err = rows.Scan(&typ, &name, &statement); err != nil {
			return err
		}
		expected, ok := want[name]
		if !ok || statement != expected || (name == "pending_identity" && typ != "index") || (name != "pending_identity" && typ != "table") {
			return ErrAuthority
		}
		n++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if n != len(want) {
		return ErrAuthority
	}
	return nil
}

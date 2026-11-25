package remoteschema

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func database(t *testing.T, records map[string]string) *sql.DB {
	t.Helper()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "bench.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec("CREATE TABLE benches (key TEXT PRIMARY KEY, record TEXT NOT NULL CHECK(json_valid(record)))"); err != nil {
		t.Fatal(err)
	}
	for k, v := range records {
		if _, err = db.Exec("INSERT INTO benches VALUES (?,?)", k, v); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func upgrade(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = Upgrade31(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestUpgrade31AssignsOnlyMissingIdentity(t *testing.T) {
	db := database(t, map[string]string{
		"a":     `{"Key":"a","Lease":{"Token":"x","Holder":"h"}}`,
		"b":     `{"Key":"b","Lease":{"Token":"y","Holder":"h"}}`,
		"blank": `{"Key":"blank","Lease":{"ID":"","Token":"z","Holder":"h"}}`,
		"kept":  `{"Key":"kept","Lease":{"ID":"0123456789abcdef0123456789abcdef","Generation":4,"Token":"w","Holder":"h"}}`,
		"free":  `{"Key":"free","Lease":null}`,
		"none":  `{"Key":"none"}`,
	})
	upgrade(t, db)
	ids := map[string]string{}
	for _, k := range []string{"a", "b", "blank", "kept"} {
		var id string
		var gen int
		if err := db.QueryRow("SELECT json_extract(record,'$.Lease.ID'),json_extract(record,'$.Lease.Generation') FROM benches WHERE key=?", k).Scan(&id, &gen); err != nil {
			t.Fatal(k, err)
		}
		if len(id) != 32 || k != "kept" && gen != 1 || k == "kept" && (gen != 4 || id != "0123456789abcdef0123456789abcdef") {
			t.Fatal(k, id, gen)
		}
		ids[id] = k
	}
	if len(ids) != 4 {
		t.Fatal("lease IDs are not per row", ids)
	}
	for k, want := range map[string]string{"free": `{"Key":"free","Lease":null}`, "none": `{"Key":"none"}`} {
		var got string
		if err := db.QueryRow("SELECT record FROM benches WHERE key=?", k).Scan(&got); err != nil || got != want {
			t.Fatal(k, got, err)
		}
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion || SchemaVersion != 31 {
		t.Fatal(version, err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = Upgrade31(tx); !errors.Is(err, ErrAuthority) {
		t.Fatal("existing audit tables were adopted", err)
	}
}

func TestAuditTablesHaveNoTokenColumn(t *testing.T) {
	db := database(t, nil)
	upgrade(t, db)
	for table, want := range map[string]string{
		"remote_certificates": "serial,key,bench_id,lease_id,generation,execution_id,holder,fingerprint,issued_at,valid_before",
		"remote_sessions":     "id,serial,key,bench_id,fingerprint,source,kind,command_name,started_at,ended_at,bytes_in,bytes_out,end_reason",
	} {
		rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for rows.Next() {
			var n string
			if err = rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			names = append(names, n)
		}
		rows.Close()
		if got := strings.Join(names, ","); got != want || strings.Contains(got, "token") {
			t.Fatalf("%s columns: %s", table, got)
		}
	}
}

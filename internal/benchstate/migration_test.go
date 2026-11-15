package benchstate

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func oldDatabase(t *testing.T, version int, b *model.Bench) *sql.DB {
	t.Helper()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "bench.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec("CREATE TABLE benches (key TEXT PRIMARY KEY, record TEXT NOT NULL CHECK(json_valid(record)))"); err != nil {
		t.Fatal(err)
	}
	if b != nil {
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("INSERT INTO benches VALUES (?, ?)", b.Key, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("PRAGMA user_version=" + strconv.Itoa(version)); err != nil {
		t.Fatal(err)
	}
	return db
}
func migrateForTest(db *sql.DB, state string, pin func(*model.Bench) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = Migrate(tx, state, pin); err != nil {
		return err
	}
	return tx.Commit()
}
func assertVersion(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("PRAGMA user_version").Scan(&got); err != nil || got != want {
		t.Fatal(got, want, err)
	}
}

func TestMigrationAdoptsScalar13AndPreservesRecords(t *testing.T) {
	b := &model.Bench{Key: "card", Repo: "/srv/repo", Path: "/srv/path", Mirror: "/srv/mirror"}
	db := oldDatabase(t, 13, b)
	if err := migrateForTest(db, t.TempDir(), func(*model.Bench) error { t.Fatal("unexpected inode migration"); return nil }); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, db, 15)
	if err := CheckVersion(db); err != nil {
		t.Fatal(err)
	}
	rows, err := Load(db)
	if err != nil || len(rows) != 1 || rows["card"].Repo != b.Repo {
		t.Fatal(rows, err)
	}
}
func TestMigrationRefusesOldSchemaAggregate(t *testing.T) {
	db := oldDatabase(t, 13, aggregate())
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("old aggregate silently adopted")
	}
	assertVersion(t, db, 13)
}
func TestMigrationRefusesActiveSupervisorAndPreservesVersion(t *testing.T) {
	b := &model.Bench{Key: "card", Path: "/srv/path", Mirror: "/srv/mirror", Lease: &model.Lease{Execution: &model.Execution{Phase: "running"}}}
	db := oldDatabase(t, 13, b)
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("active supervisor upgraded")
	}
	assertVersion(t, db, 13)
}
func TestOlderScalarMigrationStillPinsInodesAndArchiveGrace(t *testing.T) {
	b := &model.Bench{Key: "card", Path: "/srv/path", Mirror: "/srv/mirror", ArchivedAt: "2026-11-15T00:00:00Z"}
	db := oldDatabase(t, 4, b)
	calls := 0
	if err := migrateForTest(db, t.TempDir(), func(b *model.Bench) error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	rows, err := Load(db)
	if err != nil || calls != 1 || rows["card"].ReapAfter == "" {
		t.Fatal(rows, calls, err)
	}
	assertVersion(t, db, 15)
}

func TestLegacyAggregateIsNotAdopted(t *testing.T) {
	state := t.TempDir()
	raw, err := json.Marshal(Database{"card": aggregate()})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(state, "benches.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := statestore.Open(filepath.Join(state, "bench.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrateForTest(db, state, nil); err == nil {
		t.Fatal("legacy aggregate silently adopted")
	}
	assertVersion(t, db, 0)
	var count int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='benches'").Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected legacy migration left durable tables", count, err)
	}
}

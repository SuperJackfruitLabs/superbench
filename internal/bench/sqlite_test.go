package bench

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteMigrationAndReadonlyStatus(t *testing.T) {
	s, r := fixture(t)
	b, e := s.Create("task", Options{Repo: r}, false)
	if e != nil {
		t.Fatal(e)
	}
	// Reconstruct the pre-SQLite format, including a live holder token.
	b.Lease = &Lease{Holder: "legacy", Token: "preserved-token"}
	raw, e := json.Marshal(database{"task": b})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(s.State, "bench.db")); e != nil {
		t.Fatal(e)
	}
	legacy := filepath.Join(s.State, "benches.json")
	if e = os.WriteFile(legacy, raw, 0600); e != nil {
		t.Fatal(e)
	}
	// A write migrates exactly once, atomically, without modifying the old file.
	if e = s.transaction(func(records database) error {
		if records["task"].Lease.Token != "preserved-token" {
			t.Fatal("lost lease")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	original, e := os.ReadFile(legacy)
	if e != nil || string(original) != string(raw) {
		t.Fatal("legacy file changed", e)
	}
	path := filepath.Join(s.State, "bench.db")
	before, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Status("task"); e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(path)
	if e != nil || string(before) != string(after) {
		t.Fatal("status changed database", e)
	}
	// Stale legacy snapshots cannot overwrite authoritative records after migration.
	write(t, legacy, "{broken")
	if _, e = s.Status("task"); e != nil {
		t.Fatal(e)
	}
}
func TestSQLiteRollbackPreservesLease(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Lease("task", "writer"); e != nil {
		t.Fatal(e)
	}
	failure := errors.New("operation failed")
	e := s.transaction(func(records database) error { records["task"].Lease = nil; return failure })
	if !errors.Is(e, failure) {
		t.Fatal(e)
	}
	states, e := s.Status("task")
	if e != nil || states[0].Lease == nil {
		t.Fatal("failed transaction released lease", e)
	}
}
func TestStatusDoesNotInitializeState(t *testing.T) {
	root := t.TempDir()
	s := Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	states, e := s.Status("")
	if e != nil || len(states) != 0 {
		t.Fatal(states, e)
	}
	if _, e = os.Stat(s.State); !os.IsNotExist(e) {
		t.Fatal("status created state directory")
	}
}
func TestFutureSQLiteSchemaFailsClosed(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	db, e := s.openSQL(false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("PRAGMA user_version=999"); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if _, e = s.Status(""); e == nil {
		t.Fatal("future schema accepted")
	}
	if _, e = s.Lease("task", "writer"); e == nil {
		t.Fatal("future schema modified")
	}
}

func TestInvalidLegacyDoesNotInitializeSchema(t *testing.T) {
	s, _ := fixture(t)
	if e := os.MkdirAll(s.State, 0700); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(s.State, "benches.json"), "null")
	if e := s.transaction(func(database) error { return nil }); e == nil {
		t.Fatal("null legacy migrated")
	}
	db, e := s.openSQL(true)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var version int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
		t.Fatal(e)
	}
	if version != 0 {
		t.Fatal("failed migration committed schema")
	}
}

func TestExecutionSchemaRequiresExplicitOrMutatingUpgrade(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	db, e := s.openSQL(false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("PRAGMA user_version=1"); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if _, e = s.Status(""); e == nil {
		t.Fatal("read-only status silently migrated schema")
	}
	if e = s.Migrate(); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Status("task"); e != nil {
		t.Fatal(e)
	}
	db, e = s.openSQL(true)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var version int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
		t.Fatal(e)
	}
	if version != schemaVersion {
		t.Fatal("execution metadata not protected by schema version")
	}
}

func TestStorageUpgradeRefusesActiveOldSupervisor(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	if e := s.transaction(func(db database) error {
		db["task"].Lease = &Lease{Holder: "writer", Token: "fixture", Execution: &Execution{Phase: "running"}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	db, e := s.openSQL(false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("PRAGMA user_version=2"); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if e = s.Migrate(); e == nil {
		t.Fatal("upgraded underneath an active old supervisor")
	}
	db, e = s.openSQL(true)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var version int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 2 {
		t.Fatal("failed upgrade changed schema", version, e)
	}
}

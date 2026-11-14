package statestore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPreservesPrivateFilePragmasAndReadOnlyAccess(t *testing.T) {
	// URI-reserved punctuation must remain part of the physical filename rather
	// than changing SQLite options or selecting a different database.
	path := filepath.Join(t.TempDir(), "state ?# name.db")
	db, e := Open(path, false)
	if e != nil {
		t.Fatal(e)
	}
	if db.Stats().MaxOpenConnections != 1 {
		t.Fatal("state connections are no longer serialized")
	}
	for pragma, want := range map[string]int{"busy_timeout": 5000, "synchronous": 2} {
		var got int
		if e = db.QueryRow("PRAGMA " + pragma).Scan(&got); e != nil || got != want {
			t.Fatal(pragma, got, e)
		}
	}
	if _, e = db.Exec("CREATE TABLE state(value TEXT); INSERT INTO state(value) VALUES ('retained')"); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("state database is not a private physical file", info, e)
	}
	ro, e := Open(path, true)
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	var value string
	if e = ro.QueryRow("SELECT value FROM state").Scan(&value); e != nil || value != "retained" {
		t.Fatal(value, e)
	}
	if _, e = ro.Exec("INSERT INTO state(value) VALUES ('must fail')"); e == nil {
		t.Fatal("read-only state allowed mutation")
	}
}

func TestReadOnlyMissingDatabaseDoesNotCreateState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if db, e := Open(path, true); e == nil {
		db.Close()
		t.Fatal("read-only access created missing state")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("read-only access created a database", e)
	}
}

func TestOpenDoesNotCreateParentOrRelaxExistingModes(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "absent", "bench.db")
	if db, e := Open(missing, false); e == nil {
		db.Close()
		t.Fatal("adapter silently created a parent state directory")
	}
	path := filepath.Join(root, "existing.db")
	if e := os.WriteFile(path, nil, 0640); e != nil {
		t.Fatal(e)
	}
	db, e := Open(path, false)
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0640 {
		t.Fatal("adapter altered existing file permissions", info, e)
	}
}

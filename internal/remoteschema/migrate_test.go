package remoteschema_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteschema"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

var leaseID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func open(t *testing.T, state string) *sql.DB {
	t.Helper()
	db, err := statestore.Open(filepath.Join(state, "bench.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func migrate(t *testing.T, db *sql.DB, state string) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = benchstate.Migrate(tx, state, func(*model.Bench) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func record(t *testing.T, db *sql.DB, key string) string {
	t.Helper()
	var raw string
	if err := db.QueryRow("SELECT record FROM benches WHERE key=?", key).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func lease(t *testing.T, db *sql.DB, key string) model.Lease {
	t.Helper()
	var b model.Bench
	if err := json.Unmarshal([]byte(record(t, db, key)), &b); err != nil || b.Lease == nil {
		t.Fatal("record has no lease", err)
	}
	return *b.Lease
}

func assertCurrent(t *testing.T, db *sql.DB) {
	t.Helper()
	var version, tables int
	if remoteschema.SchemaVersion != benchstate.SchemaVersion {
		t.Fatal("remote schema and state schema disagree")
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 31 {
		t.Fatal("version", version, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('remote_certificates','remote_sessions')").Scan(&tables); err != nil || tables != 2 {
		t.Fatal("audit tables", tables, err)
	}
	if err := benchstate.CheckVersion(db); err != nil {
		t.Fatal(err)
	}
}

func scalar(key string, l *model.Lease) *model.Bench {
	return &model.Bench{Key: key, Repo: "/srv/repo", Path: "/srv/" + key, Mirror: "/srv/mirror", Lease: l}
}

func thirty(t *testing.T, rows ...*model.Bench) (*sql.DB, string) {
	t.Helper()
	state := t.TempDir()
	db := open(t, state)
	// A schema 30 database is the current one without the audit tables.
	migrate(t, db, state)
	if _, err := db.Exec("DROP TABLE remote_certificates; DROP TABLE remote_sessions"); err != nil {
		t.Fatal(err)
	}
	for i, b := range rows {
		b.RegistrationID = strings.Repeat(string(rune('a'+i)), 32) // Schema 30 records carry one.
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("INSERT INTO benches VALUES (?,?)", b.Key, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version=30"); err != nil {
		t.Fatal(err)
	}
	return db, state
}

func TestUpgradeFromThirtyAssignsLeaseIdentity(t *testing.T) {
	db, state := thirty(t, scalar("leased", &model.Lease{Token: "secret-token-value", Holder: "writer", Continuation: strings.Repeat("c", 32)}), scalar("free", nil))
	free := record(t, db, "free")
	migrate(t, db, state)
	assertCurrent(t, db)
	l := lease(t, db, "leased")
	if !leaseID.MatchString(l.ID) || l.Generation != 1 || l.Token != "secret-token-value" || l.Holder != "writer" || l.Continuation != strings.Repeat("c", 32) {
		t.Fatal("leased record after upgrade", l.ID, l.Generation, l.Holder)
	}
	if got := record(t, db, "free"); got != free {
		t.Fatalf("unleased record changed:\n%s\n%s", free, got)
	}
	rows, err := benchstate.Load(db)
	if err != nil || rows["leased"].Lease.ID != l.ID {
		t.Fatal("upgraded state does not load", err)
	}
}

func TestUpgradeFromLegacyReachesThirtyOne(t *testing.T) {
	state := t.TempDir()
	raw, err := json.Marshal(map[string]*model.Bench{"leased": scalar("leased", &model.Lease{Token: "legacy-token", Holder: "writer"}), "free": scalar("free", nil)})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(state, "benches.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	db := open(t, state)
	migrate(t, db, state)
	assertCurrent(t, db)
	if l := lease(t, db, "leased"); !leaseID.MatchString(l.ID) || l.Generation != 1 || l.Token != "legacy-token" {
		t.Fatal("legacy lease after upgrade", l.ID, l.Generation)
	}
}

func TestUpgradeIsIdempotentAtThirtyOne(t *testing.T) {
	db, state := thirty(t, scalar("leased", &model.Lease{Token: "t", Holder: "writer"}), scalar("free", nil))
	migrate(t, db, state)
	first := record(t, db, "leased")
	migrate(t, db, state)
	assertCurrent(t, db)
	if got := record(t, db, "leased"); got != first {
		t.Fatalf("second migration changed the lease:\n%s\n%s", first, got)
	}
}

// A schema older than 30 takes the long path, which also ends in Upgrade31.
func TestUpgradeFromTwentyNineReachesThirtyOne(t *testing.T) {
	db, state := thirty(t, scalar("leased", &model.Lease{Token: "t", Holder: "writer"}))
	if _, err := db.Exec("UPDATE benches SET record=json_remove(record,'$.RegistrationID'); PRAGMA user_version=29"); err != nil {
		t.Fatal(err)
	}
	migrate(t, db, state)
	assertCurrent(t, db)
	if l := lease(t, db, "leased"); !leaseID.MatchString(l.ID) || l.Generation != 1 {
		t.Fatal("lease after upgrade from 29", l.ID, l.Generation)
	}
}

// An older schema never adopts remote certificate or session authority: the
// migration refuses, and the caller's rollback leaves the state as it was.
func TestUpgradeRefusesRemoteTablesBelowThirtyOne(t *testing.T) {
	for _, version := range []int{30, 29} {
		for _, table := range []string{"remote_certificates", "remote_sessions"} {
			db, state := thirty(t, scalar("leased", &model.Lease{Token: "t", Holder: "writer"}))
			if _, err := db.Exec("UPDATE benches SET record=json_remove(record,'$.RegistrationID') WHERE ?=29; CREATE TABLE "+table+"(serial INTEGER); INSERT INTO "+table+" VALUES (7); PRAGMA user_version="+strconv.Itoa(version), version); err != nil {
				t.Fatal(err)
			}
			before := record(t, db, "leased")
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = benchstate.Migrate(tx, state, func(*model.Bench) error { return nil }); !errors.Is(err, remoteschema.ErrAuthority) {
				t.Fatal(version, table, "adopted", err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			var got int
			if err = db.QueryRow("PRAGMA user_version").Scan(&got); err != nil || got != version || record(t, db, "leased") != before {
				t.Fatal(version, table, "refusal changed state", got, err)
			}
		}
	}
}

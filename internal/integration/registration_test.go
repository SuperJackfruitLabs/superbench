package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
)

func TestScalarRegistrationSurvivesHandoffAndRecoveryGetsNewIdentity(t *testing.T) {
	s, b, remote, _ := fixture(t)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(b.RegistrationID) {
		t.Fatal("creation lacks registration identity")
	}
	held, err := s.Lease("task", "writer")
	if err != nil {
		t.Fatal(err)
	}
	cp, err := s.Checkpoint("task", held.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if cp.RegistrationID != b.RegistrationID {
		t.Fatal("checkpoint replaced identity")
	}
	if err = s.Release("task", held.Lease.Token); err != nil {
		t.Fatal(err)
	}
	archived, err := s.ArchiveWithGrace("task", 0)
	if err != nil {
		t.Fatal(err)
	}
	if archived.RegistrationID != b.RegistrationID {
		t.Fatal("archive replaced identity")
	}
	root := t.TempDir()
	other := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	recovered, err := other.CreateColdContext(context.Background(), "task", bench.Options{Repo: remote, Branch: b.Branch}, true)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.RegistrationID == b.RegistrationID || recovered.RegistrationID == "" {
		t.Fatal("fresh recovered registration reused source identity")
	}
}

func TestRegistrationMigrationRollbackIsWholeDatabase(t *testing.T) {
	s, b, _, _ := fixture(t)
	db, err := sql.Open("sqlite", filepath.Join(s.State, "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b.RegistrationID = ""
	b.Key = "zother"
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE benches SET record=json_remove(record,'$.RegistrationID'); PRAGMA user_version=29`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO benches(key,record) VALUES ('zother',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TRIGGER refuse_identity BEFORE UPDATE ON benches WHEN NEW.key='zother' BEGIN SELECT RAISE(ABORT,'injected second-row failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(); err == nil {
		t.Fatal("migration ignored second-row failure")
	}
	var count, version int
	if err = db.QueryRow(`SELECT count(*) FROM benches WHERE json_type(record,'$.RegistrationID') IS NOT NULL`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial identities committed", count, err)
	}
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 29 {
		t.Fatal("failed migration advanced version", version, err)
	}
	if _, err = db.Exec("DROP TRIGGER refuse_identity"); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(); err != nil {
		t.Fatal(err)
	}
	var distinct int
	if err = db.QueryRow(`SELECT count(DISTINCT json_extract(record,'$.RegistrationID')) FROM benches`).Scan(&distinct); err != nil || distinct != 2 {
		t.Fatal("registration IDs not independently assigned", distinct, err)
	}
	// No read-only projection may create a missing state directory.
	missing := bench.Store{State: filepath.Join(t.TempDir(), "missing"), Data: s.Data}
	_, _ = missing.Status("")
	if _, err = os.Stat(missing.State); !os.IsNotExist(err) {
		t.Fatal("read-only status created state", err)
	}
}

func TestReusedScalarKeyGetsFreshRegistrationAndPreservesOldCheckout(t *testing.T) {
	s, old, remote, _ := fixture(t)
	// Manufacture an ended registration without adding a product deletion API.
	// Preserve its checkout/index, refs and creation evidence separately.
	preserved := filepath.Join(filepath.Dir(old.Path), "retained-original")
	git(t, old.Mirror, "worktree", "move", old.Path, preserved)
	journal := filepath.Join(s.State, "create-task.json")
	if err := os.Rename(journal, journal+".retained"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(s.State, "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM benches WHERE key='task'"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := s.Create("task", bench.Options{Repo: remote, Branch: "bench/task-replacement"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if next.RegistrationID == old.RegistrationID || next.RegistrationID == "" {
		t.Fatal("reused key followed historical identity")
	}
	if _, err = os.Stat(filepath.Join(preserved, "file")); err != nil {
		t.Fatal("original checkout lost", err)
	}
	if _, err = os.Stat(journal + ".retained"); err != nil {
		t.Fatal("original intent lost", err)
	}
}

package benchstate

import (
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/initrepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
)

func TestSchema20MigrationPreservesRawAuthorityAndRepairJournal(t *testing.T) {
	b := readyAggregate(t)
	db := oldDatabase(t, 20, b)
	seedHistory18(t, db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := initrepair.Schema(tx); err != nil {
		t.Fatal(err)
	}
	r := initrepair.Record{BenchID: b.Workspace.ID, ID: "repair1", Key: b.Key, Digest: strings.Repeat("2", 64), OwnerDigest: strings.Repeat("3", 64), Plan: []byte("{\n \"Version\":\"reviewed\"\n}"), CreatedAt: time.Now().UTC(), Status: "active"}
	if err := initrepair.Start(tx, r); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := databaseAuthority(t, db)
	if _, err := LoadKinds(db, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, db, 21)
	if before != databaseAuthority(t, db) {
		t.Fatal("migration rewrote existing authority")
	}
	got, err := initrepair.Get(db, r.BenchID, r.ID)
	if err != nil || string(got.Plan) != string(r.Plan) || got.OwnerDigest != r.OwnerDigest || got.Status != r.Status {
		t.Fatal("repair approval changed", err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM workspace_ref_transfers").Scan(&count); err != nil || count != 0 {
		t.Fatal("migration fabricated transfer", err)
	}
}
func TestSchema20ActiveSupervisorRefusesUpgradeWithoutWrites(t *testing.T) {
	for _, phase := range []string{"starting", "running"} {
		t.Run(phase, func(t *testing.T) {
			b := readyAggregate(t)
			b.Lease = operationalLease()
			b.Lease.Execution = trackedExecution()
			b.Lease.Execution.Phase = phase
			if phase == "running" {
				b.Lease.Execution.PID = 10
			}
			db := oldDatabase(t, 20, b)
			seedHistory18(t, db)
			before := databaseAuthority(t, db)
			if err := migrateForTest(db, t.TempDir(), nil); err == nil {
				t.Fatal("active supervisor migrated")
			}
			assertVersion(t, db, 20)
			if before != databaseAuthority(t, db) {
				t.Fatal("refused upgrade changed authority")
			}
			var n int
			if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='workspace_ref_transfers'").Scan(&n); err != nil || n != 0 {
				t.Fatal("refused upgrade created journal", err)
			}
		})
	}
}
func TestHistoricalSchemasRefuseTransferMarkersAndJournal(t *testing.T) {
	for _, version := range []int{14, 17, 18, 19, 20} {
		b := outgoing(t)
		db := oldDatabase(t, version, b)
		if _, err := LoadKinds(db, t.TempDir()); err == nil {
			t.Fatal("old kind read adopted transfer", version)
		}
		if err := migrateForTest(db, t.TempDir(), nil); err == nil {
			t.Fatal("old migration adopted transfer", version)
		}
		assertVersion(t, db, version)
	}
	db := oldDatabase(t, 20, nil)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := reftransfer.Schema(tx); err != nil {
		t.Fatal(err)
	}
	r := reftransfer.Record{ID: "transfer1", SourceID: strings.Repeat("1", 32), SourceKey: "source", TargetID: strings.Repeat("2", 32), TargetKey: "target", CreationID: "creation1", Digest: strings.Repeat("3", 64), Plan: []byte(`{"Version":"future"}`), CreatedAt: time.Now().UTC(), Phase: "started"}
	if err := reftransfer.Start(tx, r); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKinds(db, t.TempDir()); err == nil {
		t.Fatal("old kind read adopted future journal")
	}
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("old migration adopted future journal")
	}
	assertVersion(t, db, 20)
	var phase string
	if err := db.QueryRow("SELECT phase FROM workspace_ref_transfers WHERE id=?", r.ID).Scan(&phase); err != nil || phase != "started" {
		t.Fatal("refusal changed approval", err)
	}
}

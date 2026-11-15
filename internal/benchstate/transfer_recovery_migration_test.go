package benchstate

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
)

func seedTransfer21(t *testing.T, db *sql.DB, version string) reftransfer.Record {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = reftransfer.Schema(tx); err != nil {
		t.Fatal(err)
	}
	r := reftransfer.Record{ID: "transfer1", SourceID: strings.Repeat("c", 32), SourceKey: "card", TargetID: strings.Repeat("e", 32), TargetKey: "target", CreationID: "transfer-create", Digest: strings.Repeat("1", 64), Plan: []byte("{\n \"Version\":\"" + version + "\"\n}\n"), CreatedAt: time.Now().UTC(), Phase: "started"}
	if err = reftransfer.Start(tx, r); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestSchema21UpgradePreservesOriginalTransferJournalAndAllExistingAuthority(t *testing.T) {
	b := outgoing(t)
	db := oldDatabase(t, 21, b)
	seedHistory18(t, db)
	r := seedTransfer21(t, db, "superbench.workspace-transfer.v1")
	before := databaseAuthority(t, db)
	if _, err := LoadKinds(db, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, db, 22)
	if before != databaseAuthority(t, db) {
		t.Fatal("migration rewrote original authority")
	}
	got, err := reftransfer.Get(db, r.ID)
	if err != nil || string(got.Plan) != string(r.Plan) || got.Phase != r.Phase || !got.CreatedAt.Equal(r.CreatedAt) {
		t.Fatal("schema21 transfer journal rewritten", err)
	}
}
func TestSchema21UpgradeRefusesActiveSupervisorBeforeAnyAuthorityChange(t *testing.T) {
	for _, phase := range []string{"starting", "running"} {
		t.Run(phase, func(t *testing.T) {
			b := readyAggregate(t)
			b.Lease = operationalLease()
			b.Lease.Execution = trackedExecution()
			b.Lease.Execution.Phase = phase
			if phase == "running" {
				b.Lease.Execution.PID = 10
			}
			db := oldDatabase(t, 21, b)
			seedHistory18(t, db)
			before := databaseAuthority(t, db)
			if err := migrateForTest(db, t.TempDir(), nil); err == nil {
				t.Fatal("active supervisor adopted schema22")
			}
			assertVersion(t, db, 21)
			if before != databaseAuthority(t, db) {
				t.Fatal("refused migration changed authority")
			}
		})
	}
}
func TestSchema21CannotAdoptFutureRetirementOrMissingApproval(t *testing.T) {
	_, b := missingRetirement(t, false)
	db := oldDatabase(t, 21, b)
	if _, err := LoadKinds(db, t.TempDir()); err == nil {
		t.Fatal("old schema kind read adopted retirement")
	}
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("old schema migration adopted retirement")
	}
	assertVersion(t, db, 21)
	db = oldDatabase(t, 21, nil)
	r := seedTransfer21(t, db, "superbench.workspace-transfer.v2")
	if _, err := LoadKinds(db, t.TempDir()); err == nil {
		t.Fatal("old schema kind read adopted missing approval")
	}
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("old schema migration adopted missing approval")
	}
	assertVersion(t, db, 21)
	got, err := reftransfer.Get(db, r.ID)
	if err != nil || string(got.Plan) != string(r.Plan) || got.Phase != "started" {
		t.Fatal("refused migration changed approval", err)
	}
}

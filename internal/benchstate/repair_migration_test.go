package benchstate

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/initrepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func TestSchema19UpgradePreservesLegitimateInterruptedHistoryBytesAndReceipts(t *testing.T) {
	b := readyAggregate(t)
	db := oldDatabase(t, 19, b)
	seedHistory18(t, db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	r, err := history.Get(tx, strings.Repeat("1", 32))
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := history.Digest(r)
	_, err = history.ObserveInterrupted(tx, r.ID, prior, history.InterruptedObservation{ApprovalDigest: strings.Repeat("2", 64), PriorDigest: prior, LogState: "absent", ObservedAt: r.StartedAt.Add(time.Minute)}, nil, 0)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := databaseAuthority(t, db)
	if _, err := LoadKinds(db, t.TempDir()); err != nil {
		t.Fatal("schema19 kind read rejected legitimate interruption", err)
	}
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, db, SchemaVersion)
	after := databaseAuthority(t, db)
	if before != after {
		t.Fatal("schema19 migration rewrote existing authority")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM initialization_repairs").Scan(&count); err != nil || count != 0 {
		t.Fatal("migration fabricated repair journal", err)
	}
}

func databaseAuthority(t *testing.T, db *sql.DB) string {
	t.Helper()
	var bench, run, receipt string
	if err := db.QueryRow("SELECT record FROM benches").Scan(&bench); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT record FROM run_history").Scan(&run); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT record FROM receipts").Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	return bench + "\x00" + run + "\x00" + receipt
}

func TestSchema19UpgradeBlocksActiveSupervisorBeforeStateOrHistoryWrites(t *testing.T) {
	for _, phase := range []string{"starting", "running"} {
		t.Run(phase, func(t *testing.T) {
			b := readyAggregate(t)
			b.Lease = operationalLease()
			b.Lease.Execution = trackedExecution()
			b.Lease.Execution.Phase = phase
			if phase == "running" {
				b.Lease.Execution.PID = 10
			}
			db := oldDatabase(t, 19, b)
			seedHistory18(t, db)
			before := databaseAuthority(t, db)
			if err := migrateForTest(db, t.TempDir(), nil); err == nil {
				t.Fatal("active schema19 supervisor upgraded")
			}
			assertVersion(t, db, 19)
			if before != databaseAuthority(t, db) {
				t.Fatal("refused migration changed existing authority")
			}
		})
	}
}

func TestHistoricalSchemasCannotAdoptFutureInitializationRepair(t *testing.T) {
	for _, version := range []int{14, 17, 18, 19} {
		b := readyAggregate(t)
		b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "repair1", Digest: strings.Repeat("1", 64), UncertainOnce: []string{"api"}}
		db := oldDatabase(t, version, b)
		if _, err := LoadKinds(db, t.TempDir()); err == nil {
			t.Fatal("historical kind read adopted future repair", version)
		}
		if err := migrateForTest(db, t.TempDir(), nil); err == nil {
			t.Fatal("historical migration adopted future repair", version)
		}
		assertVersion(t, db, version)
	}
	db := oldDatabase(t, 19, nil)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := initrepair.Schema(tx); err != nil {
		t.Fatal(err)
	}
	if err := initrepair.Start(tx, initrepair.Record{BenchID: strings.Repeat("1", 32), ID: "repair1", Key: "card", Digest: strings.Repeat("2", 64), OwnerDigest: strings.Repeat("3", 64), Plan: json.RawMessage(`{"Version":"future"}`), CreatedAt: time.Now().UTC(), Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKinds(db, t.TempDir()); err == nil {
		t.Fatal("historical kind read adopted repair journal")
	}
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("historical migration adopted repair journal")
	}
	assertVersion(t, db, 19)
}

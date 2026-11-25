package benchstate

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"strings"
	"testing"
)

func TestLatestPreservesLegitimate22AndCreatesEmptyOperationAuthority(t *testing.T) {
	db := oldDatabase(t, 22, aggregate())
	var before string
	if err := db.QueryRow(`SELECT record FROM benches`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, db, SchemaVersion)
	var after string
	if err := db.QueryRow(`SELECT json_remove(record,'$.Lease.ID','$.Lease.Generation') FROM benches`).Scan(&after); err != nil || before != after {
		t.Fatal("record changed", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM workspace_operations`).Scan(&n); err != nil || n != 0 {
		t.Fatal("authority inherited", n, err)
	}
}
func TestOldSchemaCannotAdoptFutureOperationBindings(t *testing.T) {
	db := oldDatabase(t, 22, aggregate())
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = operationcontrol.Schema(tx); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DROP TABLE workspace_operation_cancellations; DROP TABLE workspace_operation_runs`); err != nil {
		t.Fatal(err)
	}
	if err = operationcontrol.Begin(tx, operationcontrol.Intent{Target: operationcontrol.Target{UID: 42, RequestID: "future", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}, ""); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("future operation adopted")
	}
	assertVersion(t, db, 22)
	if _, err = LoadKinds(db, t.TempDir()); err == nil {
		t.Fatal("kind preflight admitted future authority")
	}
}
func TestSchema22ActiveSupervisorStillRequiresInstalledBinaryStop(t *testing.T) {
	b := aggregate()
	b.Lease.Execution = &model.Execution{Phase: "running"}
	db := oldDatabase(t, 22, b)
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("active old supervisor adopted")
	}
	assertVersion(t, db, 22)
}

func TestOldSchemaCannotAdoptEmptyFutureOperationTables(t *testing.T) {
	db := oldDatabase(t, 22, aggregate())
	var before string
	if err := db.QueryRow(`SELECT record FROM benches`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = operationcontrol.Schema(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = migrateForTest(db, t.TempDir(), nil); !errors.Is(err, operationcontrol.ErrAuthority) {
		t.Fatal("empty future tables were adopted", err)
	}
	assertVersion(t, db, 22)
	if _, err = LoadKinds(db, t.TempDir()); !errors.Is(err, operationcontrol.ErrAuthority) {
		t.Fatal("kind preflight adopted empty future tables", err)
	}
	var after string
	if err = db.QueryRow(`SELECT record FROM benches`).Scan(&after); err != nil || before != after {
		t.Fatal("refusal changed historical record", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('workspace_operations','workspace_operation_runs')`).Scan(&count); err != nil || count != 2 {
		t.Fatal("refusal removed future evidence", count, err)
	}
}

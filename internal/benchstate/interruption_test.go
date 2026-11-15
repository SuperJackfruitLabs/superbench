package benchstate

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
)

func seedHistory18(t *testing.T, db *sql.DB) string {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := history.Schema(tx); err != nil {
		t.Fatal(err)
	}
	r := history.Record{ID: strings.Repeat("1", 32), Origin: "supervised", Key: "card", Holder: "worker", StartedAt: time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO run_history(id,record) VALUES(?,?)", r.ID, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("CREATE TABLE receipts (id TEXT PRIMARY KEY,record TEXT NOT NULL); INSERT INTO receipts VALUES('retained','replay-protection')"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSchema18UpgradePreservesExactHistoryStateAndReceipts(t *testing.T) {
	b := readyAggregate(t)
	db := oldDatabase(t, 18, b)
	beforeHistory := seedHistory18(t, db)
	raw, _ := json.MarshalIndent(b, "", "  ")
	if _, err := db.Exec("UPDATE benches SET record=? WHERE key=?", string(raw), b.Key); err != nil {
		t.Fatal(err)
	}
	if err := CheckVersion(db); err == nil {
		t.Fatal("schema18 read accepted without explicit migration")
	}
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, db, 22)
	if err := CheckVersion(db); err != nil {
		t.Fatal(err)
	}
	var afterHistory, afterBench, receipt string
	if err := db.QueryRow("SELECT record FROM run_history").Scan(&afterHistory); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT record FROM benches").Scan(&afterBench); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT record FROM receipts").Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if afterHistory != beforeHistory || afterBench != string(raw) || receipt != "replay-protection" {
		t.Fatal("schema18 migration rewrote authority or receipt evidence")
	}
}

func TestSchema18ActiveSupervisorRefusesUpgradeWithoutEvidenceChanges(t *testing.T) {
	for _, phase := range []string{"starting", "running"} {
		t.Run(phase, func(t *testing.T) {
			b := readyAggregate(t)
			b.Lease = operationalLease()
			b.Lease.Execution = trackedExecution()
			b.Lease.Execution.Phase = phase
			db := oldDatabase(t, 18, b)
			beforeHistory := seedHistory18(t, db)
			var before string
			if err := db.QueryRow("SELECT record FROM benches").Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := migrateForTest(db, t.TempDir(), nil); err == nil {
				t.Fatal("active schema18 supervisor upgraded")
			}
			assertVersion(t, db, 18)
			var after, afterHistory string
			if err := db.QueryRow("SELECT record FROM benches").Scan(&after); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT record FROM run_history").Scan(&afterHistory); err != nil {
				t.Fatal(err)
			}
			if before != after || beforeHistory != afterHistory {
				t.Fatal("refused migration changed supervisor/history evidence")
			}
		})
	}
}

func TestHistoricalSchemasCannotAdoptInterruptedObservation(t *testing.T) {
	for _, version := range []int{13, 17, 18} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			db := oldDatabase(t, version, nil)
			seedHistory18(t, db)
			// It is a future authority field regardless of whether its contents are
			// structurally valid; migration must not legitimize an injected record.
			if _, err := db.Exec("UPDATE run_history SET record=json_set(record,'$.Interruption',json(?))", `{"ApprovalDigest":"untrusted"}`); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadKinds(db, t.TempDir()); err == nil {
				t.Fatal("historical kind read adopted interruption")
			}
			if err := migrateForTest(db, t.TempDir(), nil); err == nil {
				t.Fatal("historical migration adopted interruption")
			}
			assertVersion(t, db, version)
		})
	}
}

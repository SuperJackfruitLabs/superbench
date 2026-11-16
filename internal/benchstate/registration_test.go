package benchstate

import (
	"encoding/json"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func TestScalarRegistrationAssignedOnlyByMigration(t *testing.T) {
	b := &model.Bench{Key: "card", Path: "/fixed/card", Mirror: "/fixed/mirror"}
	db := oldDatabase(t, 29, b)
	var before string
	if err := db.QueryRow("SELECT record FROM benches").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	var raw, again string
	var changesBefore, changesAfter int64
	if err := db.QueryRow("SELECT record, total_changes() FROM benches").Scan(&raw, &changesBefore); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := json.Unmarshal(fields["RegistrationID"], &id); err != nil || len(id) != 32 {
		t.Fatalf("missing durable registration: %q %v", id, err)
	}
	delete(fields, "RegistrationID")
	var prior map[string]json.RawMessage
	if err := json.Unmarshal([]byte(before), &prior); err != nil {
		t.Fatal(err)
	}
	restored, _ := json.Marshal(fields)
	original, _ := json.Marshal(prior)
	if string(restored) != string(original) {
		t.Fatal("migration changed scalar fields other than identity")
	}
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT record, total_changes() FROM benches WHERE (SELECT count(*) FROM benches)=1").Scan(&again, &changesAfter); err != nil || again != raw || changesAfter != changesBefore {
		t.Fatal("repeated migration changed bytes or performed writes", err)
	}
}

func TestRegistrationMigrationPreservesAggregateAndOtherTables(t *testing.T) {
	db := oldDatabase(t, 29, aggregate())
	beforeHistory := seedHistory18(t, db)
	var before string
	if err := db.QueryRow("SELECT record FROM benches").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	var after, receipt, afterHistory string
	if err := db.QueryRow("SELECT record FROM benches").Scan(&after); err != nil || before != after {
		t.Fatal("aggregate changed", err)
	}
	if err := db.QueryRow("SELECT record FROM receipts").Scan(&receipt); err != nil || receipt != "replay-protection" {
		t.Fatal("receipt changed", err)
	}
	if err := db.QueryRow("SELECT record FROM run_history").Scan(&afterHistory); err != nil || afterHistory != beforeHistory {
		t.Fatal("history changed", err)
	}
}

func TestRegistrationMigrationRefusesFutureOrActiveAndRollsBackIgnoredWrite(t *testing.T) {
	for _, kind := range []string{"future", "empty-future", "layout-future", "root-future", "active", "ignored"} {
		t.Run(kind, func(t *testing.T) {
			b := &model.Bench{Key: "card", Path: "/fixed/card", Mirror: "/fixed/mirror"}
			if kind == "future" {
				b.RegistrationID = "0123456789abcdef0123456789abcdef"
			}
			if kind == "layout-future" {
				b.PayloadLayoutVersion = "superbench.payload-layout.v1"
			}
			if kind == "root-future" {
				b.AllocationRoot = "/data/benches/card"
			}
			if kind == "active" {
				b.Lease = &model.Lease{Execution: &model.Execution{Phase: "starting"}}
			}
			db := oldDatabase(t, 29, b)
			if kind == "empty-future" {
				if _, err := db.Exec(`UPDATE benches SET record=json_set(record,'$.RegistrationID','')`); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "ignored" {
				if _, err := db.Exec(`CREATE TRIGGER ignore_identity BEFORE UPDATE ON benches BEGIN SELECT RAISE(IGNORE); END`); err != nil {
					t.Fatal(err)
				}
			}
			var before, after string
			if err := db.QueryRow("SELECT record FROM benches").Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := migrateForTest(db, t.TempDir(), nil); err == nil {
				t.Fatal("unsafe migration accepted")
			}
			assertVersion(t, db, 29)
			if err := db.QueryRow("SELECT record FROM benches").Scan(&after); err != nil || before != after {
				t.Fatal("failed migration changed row", err)
			}
		})
	}
}

func TestRegistrationReadValidationAndImmutableTransition(t *testing.T) {
	scalar := func(key, id string) *model.Bench {
		return &model.Bench{RegistrationID: id, Key: key, Path: "/fixed/" + key, Mirror: "/fixed/mirror"}
	}
	id := "0123456789abcdef0123456789abcdef"
	a := scalar("a", id)
	for _, other := range []string{"", "INVALID", id} {
		if err := Validate(Database{"a": a, "b": scalar("b", other)}); err == nil {
			t.Fatal("invalid/duplicate registration accepted", other)
		}
	}
	rows := Database{"a": a}
	before := Capture(rows)
	a.RegistrationID = "fedcba9876543210fedcba9876543210"
	if err := ValidateTransition(before, rows); err == nil {
		t.Fatal("identity replacement accepted")
	}
	a.RegistrationID = ""
	if err := ValidateTransition(before, rows); err == nil {
		t.Fatal("retained identity erasure accepted")
	}
	b := aggregate()
	b.RegistrationID = id
	if err := Validate(Database{b.Key: b}); err == nil {
		t.Fatal("aggregate given second identity")
	}
	old := oldDatabase(t, 29, scalar("a", ""))
	var raw, after string
	if err := old.QueryRow("SELECT record FROM benches").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(old); err != nil {
		t.Fatal(err)
	}
	if err := CheckVersion(old); err == nil {
		t.Fatal("old schema admitted as current")
	}
	if err := old.QueryRow("SELECT record FROM benches").Scan(&after); err != nil || after != raw {
		t.Fatal("reader minted identity", err)
	}
}

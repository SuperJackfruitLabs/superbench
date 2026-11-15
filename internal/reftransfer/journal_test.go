package reftransfer

import (
	"bytes"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func fixture(t *testing.T) (*sql.DB, Record) {
	t.Helper()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "journal.db"), false)
	must(t, err)
	t.Cleanup(func() { db.Close() })
	must(t, transaction(db, Schema))
	return db, Record{ID: "transfer1", SourceID: strings.Repeat("a", 32), SourceKey: "source", TargetID: strings.Repeat("b", 32), TargetKey: "target", CreationID: "creation1", Digest: strings.Repeat("c", 64), Plan: []byte("{\n  \"Version\": \"reviewed\"\n}\n"), CreatedAt: time.Now().UTC(), Phase: "started"}
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func transaction(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func TestExactApprovalAndIrreversibleProgress(t *testing.T) {
	db, r := fixture(t)
	must(t, transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }))
	must(t, transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }))
	got, err := Get(db, r.ID)
	must(t, err)
	if !bytes.Equal(got.Plan, r.Plan) || !got.CreatedAt.Equal(r.CreatedAt) {
		t.Fatal("approval rewritten")
	}
	for _, change := range []func(*Record){func(r *Record) { r.Plan = bytes.TrimSpace(r.Plan) }, func(r *Record) { r.TargetKey = "other" }, func(r *Record) { r.CreatedAt = r.CreatedAt.Add(time.Second) }, func(r *Record) { r.Digest = strings.Repeat("d", 64) }} {
		changed := r
		change(&changed)
		if transaction(db, func(tx *sql.Tx) error { return Start(tx, changed) }) == nil {
			t.Fatal("approval rebound")
		}
	}
	if transaction(db, func(tx *sql.Tx) error { return Complete(tx, r.ID, r.Digest) }) == nil {
		t.Fatal("skipped detach confirmation")
	}
	if transaction(db, func(tx *sql.Tx) error { return Relinquish(tx, r.ID, strings.Repeat("e", 64)) }) == nil {
		t.Fatal("wrong approval progressed")
	}
	tx, err := db.Begin()
	must(t, err)
	must(t, Relinquish(tx, r.ID, r.Digest))
	must(t, tx.Rollback())
	got, err = Get(db, r.ID)
	must(t, err)
	if got.Phase != "started" {
		t.Fatal("rollback lost source authority")
	}
	must(t, transaction(db, func(tx *sql.Tx) error { return Relinquish(tx, r.ID, r.Digest) }))
	must(t, transaction(db, func(tx *sql.Tx) error { return Relinquish(tx, r.ID, r.Digest) }))
	if transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }) == nil {
		t.Fatal("advanced transfer restarted")
	}
	must(t, transaction(db, func(tx *sql.Tx) error { return Complete(tx, r.ID, r.Digest) }))
	must(t, transaction(db, func(tx *sql.Tx) error { return Complete(tx, r.ID, r.Digest) }))
	if transaction(db, func(tx *sql.Tx) error { return Relinquish(tx, r.ID, r.Digest) }) == nil {
		t.Fatal("completed transfer reversed")
	}
	got, err = Get(db, r.ID)
	must(t, err)
	if !bytes.Equal(got.Plan, r.Plan) || got.Phase != "completed" {
		t.Fatal("terminal approval changed")
	}
}
func TestJournalReservesSourceAndDestinationEvenAfterCompletion(t *testing.T) {
	for _, field := range []string{"sourceID", "sourceKey", "targetID", "targetKey"} {
		t.Run(field, func(t *testing.T) {
			db, r := fixture(t)
			must(t, transaction(db, func(tx *sql.Tx) error {
				if err := Start(tx, r); err != nil {
					return err
				}
				if err := Relinquish(tx, r.ID, r.Digest); err != nil {
					return err
				}
				return Complete(tx, r.ID, r.Digest)
			}))
			next := r
			next.ID = "transfer2"
			next.SourceID = strings.Repeat("d", 32)
			next.SourceKey = "source2"
			next.TargetID = strings.Repeat("e", 32)
			next.TargetKey = "target2"
			switch field {
			case "sourceID":
				next.SourceID = r.SourceID
			case "sourceKey":
				next.SourceKey = r.SourceKey
			case "targetID":
				next.TargetID = r.TargetID
			case "targetKey":
				next.TargetKey = r.TargetKey
			}
			if transaction(db, func(tx *sql.Tx) error { return Start(tx, next) }) == nil {
				t.Fatal("reserved identity reused")
			}
		})
	}
}
func TestJournalRejectsAmbiguousPrivateAndOversizedApproval(t *testing.T) {
	for _, plan := range []string{`null`, `[]`, `{"LeaseToken":"secret"}`, `{"nested":[{"Continuation":"secret"}]}`, `{"args":["private"]}`, `{"env":{}}`, `{"x":{"token":"secret"},"x":{}}`, `{"nested":{"a":1,"a":2}}`} {
		t.Run(plan, func(t *testing.T) {
			db, r := fixture(t)
			r.Plan = []byte(plan)
			if transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }) == nil {
				t.Fatal("unsafe approval accepted")
			}
		})
	}
	db, r := fixture(t)
	r.Plan = []byte(`{"x":"` + strings.Repeat("x", MaxPlan) + `"}`)
	if transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }) == nil {
		t.Fatal("oversized approval accepted")
	}
}
func TestJournalBudgetPreservesAllPriorEvidence(t *testing.T) {
	db, r := fixture(t)
	must(t, transaction(db, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare("INSERT INTO workspace_ref_transfers(id,source_id,source_key,target_id,target_key,creation_id,digest,plan,created,phase) VALUES(?,?,?,?,?,?,?,?,?,?)")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := 0; i < MaxRecords; i++ {
			if _, err := stmt.Exec(fmt.Sprint(i), fmt.Sprintf("%032x", i), fmt.Sprint("s", i), fmt.Sprintf("%032x", i+MaxRecords), fmt.Sprint("t", i), r.CreationID, r.Digest, []byte(r.Plan), r.CreatedAt.Format(time.RFC3339Nano), "completed"); err != nil {
				return err
			}
		}
		return nil
	}))
	if transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }) == nil {
		t.Fatal("exhausted journal admitted transfer")
	}
	var n int
	must(t, db.QueryRow("SELECT count(*) FROM workspace_ref_transfers").Scan(&n))
	if n != MaxRecords {
		t.Fatal("evidence pruned")
	}
}

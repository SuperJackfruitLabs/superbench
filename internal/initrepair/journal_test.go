package initrepair

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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := transaction(db, Schema); err != nil {
		t.Fatal(err)
	}
	return db, Record{BenchID: strings.Repeat("a", 32), ID: "repair1", Key: "card", Digest: strings.Repeat("b", 64), OwnerDigest: strings.Repeat("e", 64), Plan: []byte("{\n  \"Version\": \"reviewed-plan\"\n}\n"), CreatedAt: time.Now().UTC(), Status: "active"}
}
func transaction(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestJournalPreservesRawApprovalAndTerminalMonotonicity(t *testing.T) {
	db, r := fixture(t)
	must(t, transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }))
	must(t, transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }))
	got, err := Get(db, r.BenchID, r.ID)
	must(t, err)
	if !bytes.Equal(got.Plan, r.Plan) || !got.CreatedAt.Equal(r.CreatedAt) {
		t.Fatal("journal rewrote exact approval")
	}
	changed := r
	changed.Plan = bytes.TrimSpace(r.Plan)
	if err := transaction(db, func(tx *sql.Tx) error { return Start(tx, changed) }); err == nil {
		t.Fatal("same identity accepted different raw plan")
	}
	changed = r
	changed.OwnerDigest = strings.Repeat("d", 64)
	if err := transaction(db, func(tx *sql.Tx) error { return Start(tx, changed) }); err == nil {
		t.Fatal("same repair rebound owner digest")
	}
	if err := transaction(db, func(tx *sql.Tx) error { return Complete(tx, r.BenchID, r.ID, strings.Repeat("c", 64)) }); err == nil {
		t.Fatal("wrong approval completed repair")
	}
	must(t, transaction(db, func(tx *sql.Tx) error { return Complete(tx, r.BenchID, r.ID, r.Digest) }))
	must(t, transaction(db, func(tx *sql.Tx) error { return Complete(tx, r.BenchID, r.ID, r.Digest) }))
	if err := transaction(db, func(tx *sql.Tx) error { return Supersede(tx, r.BenchID, r.ID, r.Digest) }); err == nil {
		t.Fatal("completed repair became superseded")
	}
	if err := transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }); err == nil {
		t.Fatal("terminal repair revived")
	}
}

func TestSupersedeAndSuccessorClaimAreAtomicAndIdentityBound(t *testing.T) {
	db, r := fixture(t)
	must(t, transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }))
	next := r
	next.ID = "repair2"
	next.PreviousID = r.ID
	next.Digest = strings.Repeat("c", 64)
	if err := transaction(db, func(tx *sql.Tx) error { return Start(tx, next) }); err == nil {
		t.Fatal("active predecessor was bypassed")
	}
	tx, err := db.Begin()
	must(t, err)
	must(t, Supersede(tx, r.BenchID, r.ID, r.Digest))
	must(t, Start(tx, next))
	must(t, tx.Rollback())
	prior, err := Get(db, r.BenchID, r.ID)
	must(t, err)
	if prior.Status != "active" {
		t.Fatal("rollback lost active predecessor")
	}
	if _, err := Get(db, r.BenchID, next.ID); err != sql.ErrNoRows {
		t.Fatal("rollback retained successor", err)
	}
	must(t, transaction(db, func(tx *sql.Tx) error {
		if err := Supersede(tx, r.BenchID, r.ID, r.Digest); err != nil {
			return err
		}
		return Start(tx, next)
	}))
	must(t, transaction(db, func(tx *sql.Tx) error { return Supersede(tx, r.BenchID, r.ID, r.Digest) }))
	if err := transaction(db, func(tx *sql.Tx) error { return Complete(tx, r.BenchID, r.ID, r.Digest) }); err == nil {
		t.Fatal("superseded repair became completed")
	}
	other := next
	other.ID = "repair3"
	other.PreviousID = ""
	if err := transaction(db, func(tx *sql.Tx) error { return Start(tx, other) }); err == nil {
		t.Fatal("two active records for one workspace")
	}
}

func TestJournalRefusesInvalidAndCapabilityBearingPlans(t *testing.T) {
	plans := []string{`null`, `[]`, `{"LeaseToken":"private"}`, `{"nested":[{"Continuation":"private"}]}`, `{"arguments":["secret"]}`, `{"ENV":{"TOKEN":"private"}}`, `{"nested":{"Token":"private"},"nested":{}}`}
	for _, plan := range plans {
		t.Run(plan, func(t *testing.T) {
			db, r := fixture(t)
			r.Plan = []byte(plan)
			if err := transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }); err == nil {
				t.Fatal("unsafe plan journaled")
			}
		})
	}
	db, r := fixture(t)
	r.Plan = append([]byte(`{"x":"`), bytes.Repeat([]byte("x"), MaxPlan)...)
	r.Plan = append(r.Plan, []byte(`"}`)...)
	if err := transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }); err == nil {
		t.Fatal("oversized approval journaled")
	}
}

func TestJournalBudgetRefusesWithoutPruningOrChangingPriorApprovals(t *testing.T) {
	db, r := fixture(t)
	must(t, transaction(db, func(tx *sql.Tx) error {
		stmt, err := tx.Prepare("INSERT INTO initialization_repairs(bench_id,id,key,digest,previous_id,owner_digest,plan,created,status) VALUES(?,?,?,?,?,?,?,?,?)")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := 0; i < MaxRecords; i++ {
			if _, err := stmt.Exec(fmt.Sprintf("%032x", i), r.ID, r.Key, r.Digest, "", r.OwnerDigest, []byte(r.Plan), r.CreatedAt.Format(time.RFC3339Nano), "completed"); err != nil {
				return err
			}
		}
		return nil
	}))
	r.BenchID = strings.Repeat("f", 32)
	if err := transaction(db, func(tx *sql.Tx) error { return Start(tx, r) }); err == nil {
		t.Fatal("full journal admitted growth")
	}
	var count int
	must(t, db.QueryRow("SELECT count(*) FROM initialization_repairs").Scan(&count))
	if count != MaxRecords {
		t.Fatal("budget enforcement pruned evidence")
	}
}

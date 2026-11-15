package operationcontrol

import (
	"database/sql"
	"testing"
)

func TestFinalizedEpochCannotControlNextRequestUsingSameLease(t *testing.T) {
	db := store(t)
	first, b := intent("finished"), record()
	commit(t, db, func(tx *sql.Tx) error {
		if err := Begin(tx, first, b.Workspace.ID); err != nil {
			return err
		}
		return Bind(tx, first, b)
	})
	token, continuation := b.Lease.Token, b.Lease.Continuation
	commit(t, db, func(tx *sql.Tx) error { return Finish(tx, first) })
	next := intent("next-request")
	commit(t, db, func(tx *sql.Tx) error {
		if err := Begin(tx, next, b.Workspace.ID); err != nil {
			return err
		}
		return Bind(tx, next, b)
	})
	if err := Require(db, next, b); err != nil {
		t.Fatal(err)
	}
	if err := Require(db, first, b); err == nil {
		t.Fatal("historical request controls later request")
	}
	if b.Lease.Token != token || b.Lease.Continuation != continuation || b.Lease.StopRequested {
		t.Fatal("finalization changed writer", b)
	}
}
func TestIgnoredFinalizationCannotRetireEpoch(t *testing.T) {
	db := store(t)
	i, b := intent("ignored-finish"), record()
	commit(t, db, func(tx *sql.Tx) error { return Begin(tx, i, b.Workspace.ID) })
	if _, err := db.Exec(`CREATE TRIGGER ignore_finish BEFORE UPDATE OF phase ON workspace_operations BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = Finish(tx, i); err == nil {
		t.Fatal("ignored cutoff transition acknowledged")
	}
}

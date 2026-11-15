package operationcontrol

import (
	"database/sql"
	"testing"
)

func TestTerminalEpochCannotDetachLaterOwnerOrSkipRevocation(t *testing.T) {
	db := store(t)
	i, b := intent("terminal"), record()
	commit(t, db, func(tx *sql.Tx) error {
		if err := Begin(tx, i, b.Workspace.ID); err != nil {
			return err
		}
		return Bind(tx, i, b)
	})
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = ConcludeCancellation(tx, i, b); err == nil {
		t.Fatal("active continuation terminalized")
	}
	tx.Rollback()
	b.Lease.StopRequested = true
	b.Lease.Continuation = ""
	old := b.Lease.Token
	b.Lease.Token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = ConcludeCancellation(tx, i, b); err == nil {
		t.Fatal("later owner terminalized")
	}
	tx.Rollback()
	b.Lease.Token = old
	commit(t, db, func(tx *sql.Tx) error { return ConcludeCancellation(tx, i, b) })
	got, err := Get(db, i.Target)
	if err != nil || got.Phase != "stopped" {
		t.Fatal(got, err)
	}
	commit(t, db, func(tx *sql.Tx) error { return ConcludeCancellation(tx, i, b) })
}
func TestUnboundCancellationNeverAdoptsExistingLease(t *testing.T) {
	db := store(t)
	i, b := intent("unbound"), record()
	commit(t, db, func(tx *sql.Tx) error { return Begin(tx, i, b.Workspace.ID) })
	token := b.Lease.Token
	commit(t, db, func(tx *sql.Tx) error { return ConcludeCancellation(tx, i, b) })
	got, err := Get(db, i.Target)
	if err != nil || got.Phase != "finished" || b.Lease.Token != token || b.Lease.StopRequested {
		t.Fatal(got, b, err)
	}
}

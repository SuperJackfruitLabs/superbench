package setup

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestReceiptsInvalidateOnConfigChangeAndRollback(t *testing.T) {
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = Schema(tx); e == nil {
		e = Record(tx, "task", "first", "run")
	}
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if done, e := Completed(db, "task", "first"); e != nil || !done {
		t.Fatal(done, e)
	}
	if done, e := Completed(db, "task", "changed"); e != nil || done {
		t.Fatal(done, e)
	}
	tx, e = db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = Record(tx, "task", "changed", "next"); e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	if done, e := Completed(db, "task", "first"); e != nil || !done {
		t.Fatal("rollback lost completed once", done, e)
	}
	if done, e := Completed(db, "other", "first"); e != nil || done {
		t.Fatal("receipt crossed bench identity", done, e)
	}
	tx, e = db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = Invalidate(tx, "task"); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if done, e := Completed(db, "task", "first"); e != nil || done {
		t.Fatal("old preparation survived a different attempted configuration", done, e)
	}
}

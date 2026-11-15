package operationcontrol

import (
	"database/sql"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
)

func store(t *testing.T) *sql.DB {
	t.Helper()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "bindings.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = Schema(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db
}
func intent(id string) Intent {
	return Intent{Target: Target{UID: 42, RequestID: id, Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "run", Epoch: strings.Repeat("b", 32)}
}
func record() *model.Bench {
	return &model.Bench{Key: "card", Workspace: &model.Workspace{ID: strings.Repeat("c", 32)}, Lease: &model.Lease{Holder: "worker", Token: strings.Repeat("d", 32), Continuation: strings.Repeat("e", 32)}}
}
func commit(t *testing.T, db *sql.DB, fn func(*sql.Tx) error) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func TestBindingIsAtomicWithLeaseAndRejectsEveryAuthorityReplacement(t *testing.T) {
	db := store(t)
	a, b := intent("a"), record()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = Begin(tx, a, ""); err != nil {
		t.Fatal(err)
	}
	if err = Bind(tx, a, b); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM workspace_operations`).Scan(&n); err != nil || n != 0 {
		t.Fatal("rollback retained authority", n, err)
	}
	commit(t, db, func(tx *sql.Tx) error {
		if err := Begin(tx, a, ""); err != nil {
			return err
		}
		return Bind(tx, a, b)
	})
	commit(t, db, func(tx *sql.Tx) error { return Require(tx, a, b) })
	for _, change := range []func(*model.Bench){func(v *model.Bench) { v.Workspace.ID = strings.Repeat("f", 32) }, func(v *model.Bench) { v.Lease.Token = strings.Repeat("f", 32) }, func(v *model.Bench) { v.Lease.Continuation = strings.Repeat("f", 32) }, func(v *model.Bench) { v.Lease.Holder = "other" }} {
		v := record()
		change(v)
		tx, _ := db.Begin()
		err := Require(tx, a, v)
		tx.Rollback()
		if !errors.Is(err, ErrAuthority) {
			t.Fatal("replacement authorized", err)
		}
	}
	replacement := a
	replacement.Epoch = strings.Repeat("f", 32)
	tx, _ = db.Begin()
	err = Require(tx, replacement, b)
	tx.Rollback()
	if !errors.Is(err, ErrAuthority) {
		t.Fatal("new controller adopted epoch", err)
	}
}
func TestLaterOperationWithSameLeaseCannotBeStoppedByOldBinding(t *testing.T) {
	db := store(t)
	a, b := intent("old"), record()
	commit(t, db, func(tx *sql.Tx) error {
		if err := Begin(tx, a, b.Workspace.ID); err != nil {
			return err
		}
		return Bind(tx, a, b)
	})
	newer := intent("new")
	newer.Epoch = strings.Repeat("f", 32)
	tx, _ := db.Begin()
	err := Begin(tx, newer, b.Workspace.ID)
	tx.Rollback()
	if !errors.Is(err, ErrBusy) {
		t.Fatal("overlapping operation admitted", err)
	}
	if _, err = db.Exec(`UPDATE workspace_operations SET phase='finished' WHERE id=?`, a.Target.ID()); err != nil {
		t.Fatal(err)
	}
	commit(t, db, func(tx *sql.Tx) error {
		if err := Begin(tx, newer, b.Workspace.ID); err != nil {
			return err
		}
		return Bind(tx, newer, b)
	})
	tx, _ = db.Begin()
	err = Require(tx, a, b)
	tx.Rollback()
	if !errors.Is(err, ErrAuthority) {
		t.Fatal("old operation matched reused token", err)
	}
	tx, _ = db.Begin()
	err = RequireContext(tx, b, nil)
	tx.Rollback()
	if !errors.Is(err, ErrBusy) {
		t.Fatal("unbound local caller bypassed active operation", err)
	}
	commit(t, db, func(tx *sql.Tx) error { return Require(tx, newer, b) })
}

func TestIgnoredBindCannotCommitLeaseWithoutBindingAndCorruptIDRefuses(t *testing.T) {
	db := store(t)
	i, b := intent("ignored"), record()
	commit(t, db, func(tx *sql.Tx) error { return Begin(tx, i, "") })
	if _, err := db.Exec(`CREATE TRIGGER ignore_binding BEFORE UPDATE OF phase ON workspace_operations BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	tx, _ := db.Begin()
	err := Bind(tx, i, b)
	tx.Rollback()
	if !errors.Is(err, ErrAuthority) {
		t.Fatal("ignored binding acknowledged", err)
	}
	if _, err = db.Exec(`DROP TRIGGER ignore_binding`); err != nil {
		t.Fatal(err)
	}
	commit(t, db, func(tx *sql.Tx) error { return Bind(tx, i, b) })
	if _, err = db.Exec(`UPDATE workspace_operations SET id=?`, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.Begin()
	err = Require(tx, i, b)
	tx.Rollback()
	if !errors.Is(err, ErrAuthority) {
		t.Fatal("corrupt operation ID adopted", err)
	}
}

package operationcontrol

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestContextAdmissionPrecedesSelectionButDoesNotAdoptOwner(t *testing.T) {
	db := store(t)
	i := intent("context")
	b := record()
	ctx, err := WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	commit(t, db, func(tx *sql.Tx) error { return AdmitContext(ctx, tx, i.Key, "") })
	found, err := Get(db, i.Target)
	if err != nil || found.Phase != "admitted" || found.TokenHash != "" {
		t.Fatal("planning admission adopted a capability", found, err)
	}
	if err = GuardContext(ctx, db, b); !errors.Is(err, ErrAuthority) {
		t.Fatal("unbound owner authorized", err)
	}
	commit(t, db, func(tx *sql.Tx) error { return BindContext(ctx, tx, b) })
	if err = GuardContext(ctx, db, b); err != nil {
		t.Fatal(err)
	}
	if err = GuardContext(context.Background(), db, b); !errors.Is(err, ErrBusy) {
		t.Fatal("unbound local caller crossed active request", err)
	}
	commit(t, db, func(tx *sql.Tx) error { return BindContext(ctx, tx, b) })
	tx, _ := db.Begin()
	err = AdmitContext(context.Background(), tx, i.Key, b.Workspace.ID)
	tx.Rollback()
	if !errors.Is(err, ErrBusy) {
		t.Fatal("local planner crossed active operation", err)
	}
}

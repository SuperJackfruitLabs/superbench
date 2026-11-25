package benchstate

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestSchema26Migrates25AndCommitsBoundMaterializationRevocation(t *testing.T) {
	db := oldDatabase(t, 25, aggregate())
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
		t.Fatal("migration changed workspace", err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 1, RequestID: "new26", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	update := func(fn func(Database, *sql.Tx) error) error {
		tx, e := db.Begin()
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = Update(tx, fn); e != nil {
			return e
		}
		return tx.Commit()
	}
	if err = update(func(rows Database, tx *sql.Tx) error {
		b := rows[i.Key]
		if e := operationcontrol.AdmitContext(ctx, tx, i.Key, b.Workspace.ID); e != nil {
			return e
		}
		return operationcontrol.BindContext(ctx, tx, b)
	}); err != nil {
		t.Fatal(err)
	}
	if err = update(func(rows Database, tx *sql.Tx) error {
		b := rows[i.Key]
		if e := operationcontrol.RequireOwner(tx, i, b); e != nil {
			return e
		}
		b.Lease.StopRequested = true
		b.Lease.Continuation = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := Load(db)
	if err != nil || !rows[i.Key].Lease.StopRequested || rows[i.Key].Lease.Continuation != "" || rows[i.Key].Workspace.Phase != "materializing" {
		t.Fatal(rows, err)
	}
}
func TestHistoricalSchemasRejectStoppedMaterializationWithoutMutation(t *testing.T) {
	for _, v := range []int{14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25} {
		b := aggregate()
		b.Lease.StopRequested = true
		b.Lease.Continuation = ""
		db := oldDatabase(t, v, b)
		if err := migrateForTest(db, t.TempDir(), nil); err == nil {
			t.Fatalf("schema%d adopted future revocation", v)
		}
		assertVersion(t, db, v)
		if _, err := LoadKinds(db, t.TempDir()); err == nil {
			t.Fatalf("schema%d kind preflight adopted future revocation", v)
		}
	}
}

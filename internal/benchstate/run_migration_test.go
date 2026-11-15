package benchstate

import (
	"strconv"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestSchema25Migrates24WithoutInventingOperationRuns(t *testing.T) {
	db := oldDatabase(t, 24, aggregate())
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = operationcontrol.Schema(tx); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DROP TABLE workspace_operation_runs`); err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 1, RequestID: "old", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	if err = operationcontrol.Begin(tx, i, ""); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, db, 25)
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM workspace_operation_runs`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	got, err := operationcontrol.Get(db, i.Target)
	if err != nil || got.Intent != i {
		t.Fatal(got, err)
	}
}

func TestOlderSchemasCannotAdoptFutureRunProvenance(t *testing.T) {
	for _, v := range []int{22, 23, 24} {
		t.Run(strconv.Itoa(v), func(t *testing.T) {
			db := oldDatabase(t, v, aggregate())
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(`CREATE TABLE workspace_operation_runs(operation_id TEXT)`); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err = migrateForTest(db, t.TempDir(), nil); err == nil {
				t.Fatal("future run authority adopted")
			}
			assertVersion(t, db, v)
			if _, err = LoadKinds(db, t.TempDir()); err == nil {
				t.Fatal("future runs admitted by kind preflight")
			}
		})
	}
}

package benchstate

import (
	"errors"
	"strconv"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestSchema27Preserves26WithoutInventingConclusions(t *testing.T) {
	db := oldDatabase(t, 26, aggregate())
	var before string
	if err := db.QueryRow(`SELECT record FROM benches`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, db, SchemaVersion)
	var after string
	if err := db.QueryRow(`SELECT record FROM benches`).Scan(&after); err != nil || before != after {
		t.Fatal("record changed", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM workspace_operation_cancellations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invented terminal authority", count, err)
	}
}
func TestHistoricalSchemasCannotAdoptFutureCancellationAuthority(t *testing.T) {
	for _, v := range []int{22, 23, 24, 25, 26} {
		t.Run(strconv.Itoa(v), func(t *testing.T) {
			db := oldDatabase(t, v, aggregate())
			if _, err := db.Exec(`CREATE TABLE workspace_operation_cancellations(operation_id TEXT PRIMARY KEY,record TEXT)`); err != nil {
				t.Fatal(err)
			}
			if err := migrateForTest(db, t.TempDir(), nil); !errors.Is(err, operationcontrol.ErrAuthority) {
				t.Fatal("future conclusion adopted", err)
			}
			assertVersion(t, db, v)
			if _, err := LoadKinds(db, t.TempDir()); !errors.Is(err, operationcontrol.ErrAuthority) {
				t.Fatal("future conclusion accepted by kind preflight", err)
			}
		})
	}
}

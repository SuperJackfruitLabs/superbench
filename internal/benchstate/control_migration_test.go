package benchstate

import (
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestSchema24Migrates23WithoutInventingManagedControl(t *testing.T) {
	b := aggregate()
	db := oldDatabase(t, 23, b)
	_, err := db.Exec(`CREATE TABLE workspace_operations(id TEXT PRIMARY KEY,owner INTEGER NOT NULL,request_id TEXT NOT NULL,digest TEXT NOT NULL,key TEXT NOT NULL,operation TEXT NOT NULL,epoch TEXT NOT NULL,bench_id TEXT NOT NULL,holder TEXT NOT NULL,token_hash TEXT NOT NULL,continuation_hash TEXT NOT NULL,phase TEXT NOT NULL,UNIQUE(owner,request_id))`)
	if err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 42, RequestID: "legacy", Digest: strings.Repeat("a", 64)}, Key: b.Key, Operation: "setup", Epoch: strings.Repeat("b", 32)}
	_, err = db.Exec(`INSERT INTO workspace_operations VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, i.Target.ID(), i.Target.UID, i.Target.RequestID, i.Target.Digest, i.Key, i.Operation, i.Epoch, b.Workspace.ID, "", "", "", "admitted")
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err = db.QueryRow(`SELECT record FROM benches`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadKinds(db, t.TempDir()); err != nil {
		t.Fatal("valid legacy kind preflight refused", err)
	}
	if err = migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, db, 24)
	got, err := operationcontrol.Get(db, i.Target)
	if err != nil || got.Control != (operationcontrol.Control{}) || got.Phase != "admitted" {
		t.Fatal("legacy control was certified or changed", got, err)
	}
	var after string
	if err = db.QueryRow(`SELECT record FROM benches`).Scan(&after); err != nil || before != after {
		t.Fatal("record changed", err)
	}
}
func TestSchema23CannotAdoptFutureManagedControl(t *testing.T) {
	db := oldDatabase(t, 23, aggregate())
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = operationcontrol.Schema(tx); err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 1, RequestID: "future", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	if err = operationcontrol.BeginWithControl(tx, i, "", operationcontrol.Control{State: "/state", Root: "/scope", HostID: strings.Repeat("c", 64), BootID: "boot"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("future control adopted")
	}
	assertVersion(t, db, 23)
	if _, err = LoadKinds(db, t.TempDir()); err == nil {
		t.Fatal("future control admitted by kind preflight")
	}
}
func TestSchema23ActiveSupervisorStillRequiresInstalledStop(t *testing.T) {
	b := aggregate()
	b.Lease.Execution = &model.Execution{Phase: "running"}
	db := oldDatabase(t, 23, b)
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("active supervisor adopted")
	}
	assertVersion(t, db, 23)
}

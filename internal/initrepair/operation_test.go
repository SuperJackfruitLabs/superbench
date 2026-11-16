package initrepair

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repaircontrol"
)

func operationFixture(t *testing.T) (*sql.DB, Record, *model.Bench, repaircontrol.Admission) {
	t.Helper()
	db, r := fixture(t)
	if err := transaction(db, func(tx *sql.Tx) error {
		if err := OperationSchema(tx); err != nil {
			return err
		}
		return operationcontrol.Schema(tx)
	}); err != nil {
		t.Fatal(err)
	}
	l, err := model.NewLease("initialization")
	if err != nil {
		t.Fatal(err)
	}
	l.Continuation, err = model.Nonce()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(l.Token))
	r.OwnerDigest = hex.EncodeToString(sum[:])
	b := &model.Bench{Key: r.Key, Workspace: &model.Workspace{ID: r.BenchID}, Lease: l}
	a := repaircontrol.Admission{Instance: strings.Repeat("1", 32), ID: strings.Repeat("2", 32), BenchID: r.BenchID, RepairID: r.ID, Approval: r.Digest, OwnerDigest: r.OwnerDigest, Intent: operationcontrol.Intent{Target: operationcontrol.Target{UID: 1001, RequestID: "repair-internal", Digest: strings.Repeat("3", 64)}, Key: r.Key, Operation: "initialization-repair", Epoch: strings.Repeat("4", 32)}, Control: operationcontrol.Control{State: "/fixture/state", Root: "/fixture/cgroups", HostID: strings.Repeat("5", 64), BootID: "fixture-boot"}}
	return db, r, b, a
}

func bindFixture(tx *sql.Tx, r Record, b *model.Bench, a repaircontrol.Admission) error {
	if err := Start(tx, r); err != nil {
		return err
	}
	if err := operationcontrol.BeginWithControl(tx, a.Intent, a.BenchID, a.Control); err != nil {
		return err
	}
	if err := operationcontrol.Bind(tx, a.Intent, b); err != nil {
		return err
	}
	return BindOperation(tx, a)
}

func TestCoreRepairOperationPinsExactIndependentAdmissionAndOwnerAtomically(t *testing.T) {
	db, r, b, a := operationFixture(t)
	if err := transaction(db, func(tx *sql.Tx) error { return bindFixture(tx, r, b, a) }); err != nil {
		t.Fatal(err)
	}
	got, err := GetOperation(db, r.BenchID, r.ID)
	if err != nil || got != a {
		t.Fatal(got, err)
	}
	if err := transaction(db, func(tx *sql.Tx) error { return BindOperation(tx, a) }); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"instance", "row", "owner", "epoch", "policy-control", "approval", "uid"} {
		t.Run(kind, func(t *testing.T) {
			changed := a
			switch kind {
			case "instance":
				changed.Instance = strings.Repeat("a", 32)
			case "row":
				changed.ID = strings.Repeat("b", 32)
			case "owner":
				changed.OwnerDigest = strings.Repeat("c", 64)
			case "epoch":
				changed.Intent.Epoch = strings.Repeat("d", 32)
			case "policy-control":
				changed.Control.Root += "-other"
			case "approval":
				changed.Approval = strings.Repeat("e", 64)
			case "uid":
				changed.Intent.Target.UID++
			}
			if err := transaction(db, func(tx *sql.Tx) error { return BindOperation(tx, changed) }); err == nil {
				t.Fatal("committed repair authority rebound")
			}
		})
	}
	// Raw canonical evidence is authority: whitespace/unknown fields cannot
	// masquerade as the originally pinned admission after reopening the state.
	raw, _ := json.Marshal(a)
	if _, err := db.Exec(`UPDATE initialization_repair_operations SET admission=?`, string(append(raw, '\n'))); err != nil {
		t.Fatal(err)
	}
	if _, err := GetOperation(db, r.BenchID, r.ID); err == nil {
		t.Fatal("changed stored admission adopted")
	}
}

func TestIgnoredOperationLinkRollsBackTheWholeClaimTuple(t *testing.T) {
	db, r, b, a := operationFixture(t)
	if _, err := db.Exec(`CREATE TRIGGER ignore_link BEFORE INSERT ON initialization_repair_operations BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if err := transaction(db, func(tx *sql.Tx) error { return bindFixture(tx, r, b, a) }); err == nil {
		t.Fatal("ignored operation binding committed")
	}
	for _, table := range []string{"initialization_repairs", "workspace_operations", "initialization_repair_operations"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial claim authority survived rollback", table, count, err)
		}
	}
}

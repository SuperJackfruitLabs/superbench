package benchstate

import (
	"encoding/json"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/initrepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestSchema28PreservesSchema27ConclusionsAndDoesNotInventRepairOperations(t *testing.T) {
	db := oldDatabase(t, 27, aggregate())
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := operationcontrol.Schema(tx); err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 1001, RequestID: "known-no-effects", Digest: strings.Repeat("1", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("2", 32)}
	if err := operationcontrol.Begin(tx, i, ""); err != nil {
		t.Fatal(err)
	}
	if err := operationcontrol.ConcludeCancellation(tx, i, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var before, conclusion string
	if err := db.QueryRow(`SELECT record FROM benches`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT record FROM workspace_operation_cancellations`).Scan(&conclusion); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKinds(db, t.TempDir()); err != nil {
		t.Fatal("legitimate 27 kind read", err)
	}
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, db, SchemaVersion)
	var after, same string
	var count int
	if err := db.QueryRow(`SELECT json_remove(record,'$.Lease.ID','$.Lease.Generation') FROM benches`).Scan(&after); err != nil || after != before {
		t.Fatal("migration changed old row", err)
	}
	if err := db.QueryRow(`SELECT record FROM workspace_operation_cancellations`).Scan(&same); err != nil || same != conclusion {
		t.Fatal("migration changed conclusion", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM initialization_repair_operations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented repair authority", err)
	}
}

func TestSchema27CannotAdoptTrackedRepairAuthorityAndActiveSupervisorStillBlocks(t *testing.T) {
	for _, kind := range []string{"empty-table", "new-kind", "marker", "active-supervisor"} {
		t.Run(kind, func(t *testing.T) {
			b := aggregate()
			if kind == "marker" {
				b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "future", Digest: strings.Repeat("1", 64), OperationDigest: strings.Repeat("2", 64)}
			}
			if kind == "active-supervisor" {
				b = readyAggregate(t)
				b.Lease = operationalLease()
				b.Lease.Execution = trackedExecution()
				b.Lease.Execution.Phase = "running"
				b.Lease.Execution.PID = 12
			}
			db := oldDatabase(t, 27, b)
			if kind == "empty-table" {
				tx, _ := db.Begin()
				if err := initrepair.OperationSchema(tx); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "new-kind" {
				tx, _ := db.Begin()
				if err := operationcontrol.Schema(tx); err != nil {
					t.Fatal(err)
				}
				i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 1001, RequestID: "future-local", Digest: strings.Repeat("a", 64)}, Key: b.Key, Operation: "initialization-repair", Epoch: strings.Repeat("b", 32)}
				if err := operationcontrol.Begin(tx, i, b.Workspace.ID); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			var before, after string
			if err := db.QueryRow(`SELECT record FROM benches`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := migrateForTest(db, t.TempDir(), nil); err == nil || kind == "active-supervisor" && !strings.Contains(err.Error(), "stop supervised") {
				t.Fatal("future authority/active supervisor adopted or wrong guard", err)
			}
			assertVersion(t, db, 27)
			if err := db.QueryRow(`SELECT record FROM benches`).Scan(&after); err != nil || before != after {
				t.Fatal("refusal changed row", err)
			}
			if kind != "active-supervisor" {
				if _, err := LoadKinds(db, t.TempDir()); err == nil {
					t.Fatal("kind preflight adopted future authority")
				}
			}
		})
	}
}

func TestMaterializationRepairTransitionRequiresFreshExplicitTrackedEpoch(t *testing.T) {
	old := aggregate()
	old.Lease.StopRequested = true
	old.Lease.Continuation = ""
	next := cloneBench(t, old)
	next.Lease.Token, next.Lease.Continuation, next.Lease.StopRequested = strings.Repeat("4", 32), strings.Repeat("5", 32), false
	next.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "materialization1", Digest: strings.Repeat("6", 64), OperationDigest: strings.Repeat("7", 64)}
	assertRepairValid(t, old, next)
	for _, kind := range []string{"no-operation", "same-token", "live-old", "source", "quota", "history", "uncertain-once"} {
		t.Run(kind, func(t *testing.T) {
			before, after := cloneBench(t, old), cloneBench(t, next)
			switch kind {
			case "no-operation":
				after.Workspace.InitializationRepair.OperationDigest = ""
			case "same-token":
				after.Lease.Token = before.Lease.Token
			case "live-old":
				before.Lease.StopRequested = false
				before.Lease.Continuation = strings.Repeat("a", 32)
			case "source":
				after.Workspace.Members[0].Source = strings.Repeat("b", 40)
			case "quota":
				after.QuotaBytes++
			case "history":
				after.Workspace.InitializationRepair.RetiredRuns = []string{strings.Repeat("8", 32)}
			case "uncertain-once":
				after.Workspace.InitializationRepair.UncertainOnce = []string{"api"}
			}
			if Validate(Database{"card": after}) == nil && ValidateTransition(Capture(Database{"card": before}), Database{"card": after}) == nil {
				t.Fatal("unsafe materialization takeover accepted")
			}
		})
	}
	// Progress preserves provenance but never mutates the exact operation digest.
	advanced := cloneBench(t, next)
	advanced.Workspace.Phase = "initializing"
	assertRepairValid(t, next, advanced)
	advanced.Workspace.InitializationRepair.OperationDigest = strings.Repeat("a", 64)
	if err := ValidateTransition(Capture(Database{"card": next}), Database{"card": advanced}); err == nil {
		t.Fatal("operation digest rebound by ordinary progress")
	}
	raw, _ := json.Marshal(next)
	var roundtrip model.Bench
	if err := json.Unmarshal(raw, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip.Workspace.InitializationRepair.OperationDigest != next.Workspace.InitializationRepair.OperationDigest {
		t.Fatal("lost operation provenance")
	}
}

package benchstate

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func readyAggregate(t *testing.T) *model.Bench {
	t.Helper()
	b := preparedAggregate()
	b.Workspace.Checkpoint = pendingCheckpoint(t, b)
	b.Workspace.Checkpoint.Seal = strings.Repeat("f", 40)
	b.Workspace.Phase = "ready"
	b.Lease = nil
	return b
}
func operationalLease() *model.Lease {
	return &model.Lease{Holder: "worker", Token: strings.Repeat("8", 32), Continuation: strings.Repeat("9", 32)}
}
func TestAcquisitionRevokesOldPreparationBeforeNewExecution(t *testing.T) {
	b := readyAggregate(t)
	rows := Database{"card": b}
	prior := copyCheckpoint(b.Workspace.Checkpoint)
	changeValid(t, rows, func(b *model.Bench) { b.Lease = operationalLease(); b.Workspace.Preparation = nil })
	if !reflect.DeepEqual(b.Workspace.Checkpoint, prior) {
		t.Fatal("acquisition changed checkpoint")
	}
	changeValid(t, rows, func(b *model.Bench) {
		b.Workspace.Preparation = []model.WorkspacePreparation{{Name: "api", Digest: strings.Repeat("2", 64)}}
	})
	changeValid(t, rows, func(b *model.Bench) { b.Lease.Execution = trackedExecution() })
	changeValid(t, rows, func(b *model.Bench) { b.Lease.Execution.Phase = "running"; b.Lease.Execution.PID = 321 })
	changeValid(t, rows, func(b *model.Bench) { b.Lease.Execution.Phase = "stopped" })
	changeValid(t, rows, func(b *model.Bench) { b.Lease = nil })
}
func TestAcquisitionCannotReuseProofsStartOrResumeOldOwnership(t *testing.T) {
	for _, kind := range []string{"proofs", "execution", "no-continuation", "stopped", "pending"} {
		t.Run(kind, func(t *testing.T) {
			b := readyAggregate(t)
			rows := Database{"card": b}
			before := Capture(rows)
			b.Lease = operationalLease()
			if kind != "proofs" {
				b.Workspace.Preparation = nil
			}
			switch kind {
			case "execution":
				b.Lease.Execution = trackedExecution()
			case "no-continuation":
				b.Lease.Continuation = ""
			case "stopped":
				b.Lease.StopRequested = true
			case "pending":
				b.Workspace.Pending = pendingCheckpoint(t, b)
			}
			if err := ValidateTransition(before, rows); err == nil {
				t.Fatal("unsafe acquisition accepted")
			}
		})
	}
	b := readyAggregate(t)
	b.Lease = operationalLease()
	b.Workspace.Preparation = nil
	rows := Database{"card": b}
	before := Capture(rows)
	b.Lease.Token = strings.Repeat("a", 32)
	if err := ValidateTransition(before, rows); err == nil {
		t.Fatal("same holder replaced held token")
	}
}
func TestRepairReleaseMayPreservePartialPreparationAfterStoppedCheckpoint(t *testing.T) {
	for _, clear := range []bool{false, true} {
		b := readyAggregate(t)
		b.Lease = operationalLease()
		b.Workspace.Preparation = nil
		rows := Database{"card": b}
		if clear {
			b.Workspace.Preparation = []model.WorkspacePreparation{{Name: "api", Digest: strings.Repeat("3", 64)}}
		}
		changeValid(t, rows, func(b *model.Bench) {
			b.Lease = nil
			if clear {
				b.Workspace.Preparation = nil
			}
		})
		if b.Workspace.Checkpoint == nil {
			t.Fatal("repair release lost checkpoint")
		}
	}
}
func TestRunningReleaseAndProofChangesRemainRejected(t *testing.T) {
	b := readyAggregate(t)
	b.Lease = operationalLease()
	b.Lease.Execution = trackedExecution()
	b.Lease.Execution.Phase = "running"
	b.Lease.Execution.PID = 123
	rows := Database{"card": b}
	before := Capture(rows)
	b.Lease = nil
	if err := ValidateTransition(before, rows); err == nil {
		t.Fatal("running writer released")
	}
	b.Lease = operationalLease()
	b.Lease.Execution = trackedExecution()
	b.Lease.Execution.Phase = "running"
	b.Lease.Execution.PID = 123
	before = Capture(rows)
	b.Workspace.Preparation = nil
	if err := ValidateTransition(before, rows); err == nil {
		t.Fatal("running writer changed preparation proof")
	}
}
func TestSchema15MigrationPreservesValidHistoricalReadyProofs(t *testing.T) {
	for _, held := range []bool{false, true} {
		b := readyAggregate(t)
		b.Workspace.Preparation[0].RunID = strings.Repeat("4", 32)
		if held {
			b.Lease = operationalLease()
		}
		db := oldDatabase(t, 15, b)
		before, _ := json.Marshal(b)
		if err := migrateForTest(db, t.TempDir(), nil); err != nil {
			t.Fatal(err)
		}
		assertVersion(t, db, SchemaVersion)
		rows, err := Load(db)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := json.Marshal(rows["card"])
		if string(before) != regexp.MustCompile(`"ID":"[a-f0-9]{32}","Generation":1,`).ReplaceAllString(string(after), "") {
			t.Fatal("migration changed historical identity, lease or proof")
		}
	}
	b := readyAggregate(t)
	b.Workspace.Preparation = nil
	db := oldDatabase(t, 15, b)
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("schema16 ready shape adopted from15")
	}
	assertVersion(t, db, 15)
}
func TestSchema15ActiveSupervisorStillBlocksMigration(t *testing.T) {
	b := readyAggregate(t)
	b.Lease = operationalLease()
	b.Lease.Execution = trackedExecution()
	db := oldDatabase(t, 15, b)
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("active supervisor upgraded")
	}
	assertVersion(t, db, 15)
}

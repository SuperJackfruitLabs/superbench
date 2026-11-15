package benchstate

import (
	"encoding/json"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

func preparedAggregate() *model.Bench {
	b := aggregate()
	b.Workspace.Phase = "prepared"
	b.Workspace.Preparation = []model.WorkspacePreparation{{Name: "api", Digest: strings.Repeat("1", 64)}}
	return b
}
func pendingCheckpoint(t *testing.T, b *model.Bench) *model.WorkspaceCheckpoint {
	t.Helper()
	c := &model.WorkspaceCheckpoint{GenerationID: strings.Repeat("e", 32), Members: []model.WorkspaceCommit{{Name: "api", Commit: b.Workspace.Members[0].Source}}}
	if b.Workspace.Checkpoint != nil {
		c.Previous = b.Workspace.Checkpoint.Seal
	}
	m, err := workspacepolicy.Manifest(b, c)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	c.Digest = checkpointset.Digest(raw)
	return c
}
func changeValid(t *testing.T, rows Database, change func(*model.Bench)) {
	t.Helper()
	before := Capture(rows)
	change(rows["card"])
	if err := Validate(rows); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTransition(before, rows); err != nil {
		t.Fatal(err)
	}
}
func TestGenesisTransitionsRequirePendingWholeVectorAndReleaseOnReady(t *testing.T) {
	rows := Database{"card": aggregate()}
	changeValid(t, rows, func(b *model.Bench) { b.Workspace.Phase = "initializing" })
	changeValid(t, rows, func(b *model.Bench) {
		b.Workspace.Preparation = []model.WorkspacePreparation{{Name: "api", Digest: strings.Repeat("1", 64)}}
		b.Workspace.Phase = "prepared"
	})
	changeValid(t, rows, func(b *model.Bench) { b.Workspace.Pending = pendingCheckpoint(t, b) })
	changeValid(t, rows, func(b *model.Bench) {
		c := copyCheckpoint(b.Workspace.Pending)
		c.Seal = strings.Repeat("f", 40)
		b.Workspace.Checkpoint = c
		b.Workspace.Pending = nil
		b.Workspace.Phase = "ready"
		b.Lease = nil
	})
	changeValid(t, rows, func(b *model.Bench) {
		b.Lease = &model.Lease{Holder: "writer", Token: strings.Repeat("9", 32), Continuation: strings.Repeat("8", 32)}
		b.Workspace.Preparation = nil
	})
}
func TestPendingCannotBeReplacedOrForgottenAndSnapshotsAreDeep(t *testing.T) {
	for _, kind := range []string{"replace", "clear", "mismatched-complete", "direct-complete", "preparation"} {
		t.Run(kind, func(t *testing.T) {
			b := preparedAggregate()
			b.Workspace.Pending = pendingCheckpoint(t, b)
			if kind == "direct-complete" {
				b.Workspace.Pending = nil
			}
			rows := Database{"card": b}
			before := Capture(rows)
			switch kind {
			case "replace":
				b.Workspace.Pending.Members[0].Commit = strings.Repeat("9", 40)
			case "clear":
				b.Workspace.Pending = nil
			case "mismatched-complete":
				b.Workspace.Checkpoint = copyCheckpoint(b.Workspace.Pending)
				b.Workspace.Checkpoint.Members[0].Commit = strings.Repeat("9", 40)
				b.Workspace.Checkpoint.Seal = strings.Repeat("f", 40)
				b.Workspace.Pending = nil
			case "direct-complete":
				b.Workspace.Checkpoint = pendingCheckpoint(t, b)
				b.Workspace.Checkpoint.Seal = strings.Repeat("f", 40)
			case "preparation":
				b.Workspace.Preparation[0].Digest = strings.Repeat("2", 64)
			}
			if err := ValidateTransition(before, rows); err == nil {
				t.Fatal("unsafe pending mutation accepted")
			}
		})
	}
}
func trackedExecution() *model.Execution {
	return &model.Execution{ID: strings.Repeat("7", 32), Root: "/sys/fs/cgroup/workspace", HostID: "test-host", BootID: "test-boot", Phase: "starting", Log: "/srv/state/run.log", LogLimitBytes: 1 << 20}
}
func TestExecutionFencingRetainsScopesAndCannotResurrectContinuation(t *testing.T) {
	b := aggregate()
	b.Workspace.Phase = "initializing"
	rows := Database{"card": b}
	changeValid(t, rows, func(b *model.Bench) {
		b.Lease.Execution = trackedExecution()
		b.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("1", 64)}
	})
	changeValid(t, rows, func(b *model.Bench) { b.Lease.Execution.Phase = "running"; b.Lease.Execution.PID = 123 })
	before := Capture(rows)
	b.Lease.Execution = nil
	if err := ValidateTransition(before, rows); err == nil {
		t.Fatal("running scope cleared")
	}
	b.Lease.Execution = trackedExecution()
	b.Lease.Execution.Phase = "running"
	b.Lease.Execution.PID = 123
	changeValid(t, rows, func(b *model.Bench) { b.Lease.Execution.Phase = "stopped" })
	before = Capture(rows)
	b.Lease.Execution.Phase = "running"
	if err := ValidateTransition(before, rows); err == nil {
		t.Fatal("stopped scope resumed")
	}
	b.Lease.Execution.Phase = "stopped"
	before = Capture(rows)
	old := b.Lease.Execution
	b.Lease.Execution = nil
	if err := ValidateTransition(before, rows); err == nil {
		t.Fatal("stopped scope discarded without retained history")
	}
	b.Lease.Execution = old
	changeValid(t, rows, func(b *model.Bench) {
		b.Lease.RetainedRuns = append(b.Lease.RetainedRuns, b.Lease.Execution.ID)
		b.Lease.Execution = nil
		b.Workspace.Stage = nil
	})
	changeValid(t, rows, func(b *model.Bench) { b.Lease.StopRequested = true; b.Lease.Continuation = "" })
	before = Capture(rows)
	b.Lease.StopRequested = false
	b.Lease.Continuation = strings.Repeat("a", 32)
	if err := ValidateTransition(before, rows); err == nil {
		t.Fatal("stopped continuation resurrected")
	}
}
func TestMigration14PreservesStagedGroupAndRejectsNewLifecycleData(t *testing.T) {
	for _, phase := range []string{"materializing", "initializing"} {
		b := aggregate()
		b.Workspace.Phase = phase
		db := oldDatabase(t, 14, b)
		if err := migrateForTest(db, t.TempDir(), nil); err != nil {
			t.Fatal(err)
		}
		assertVersion(t, db, 19)
	}
	b := preparedAggregate()
	db := oldDatabase(t, 14, b)
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("new lifecycle was adopted from14")
	}
	assertVersion(t, db, 14)
}
func TestLoadKindsRecognizesHistorical14GroupWithoutMigration(t *testing.T) {
	db := oldDatabase(t, 14, aggregate())
	rows, err := LoadKinds(db, t.TempDir())
	if err != nil || rows["card"].Workspace == nil {
		t.Fatal(rows, err)
	}
	assertVersion(t, db, 14)
	db2 := oldDatabase(t, 14, preparedAggregate())
	if _, err := LoadKinds(db2, t.TempDir()); err == nil {
		t.Fatal("historical kind inspection accepted schema15 lifecycle")
	}
}

func TestStageMetadataIsPinnedToExecutionUntilStoppedRetention(t *testing.T) {
	for _, kind := range []string{"change", "clear", "forget-stopped"} {
		t.Run(kind, func(t *testing.T) {
			b := aggregate()
			b.Workspace.Phase = "initializing"
			b.Lease.Execution = trackedExecution()
			b.Lease.Execution.Phase = "stopped"
			b.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("1", 64)}
			rows := Database{"card": b}
			before := Capture(rows)
			switch kind {
			case "change":
				b.Workspace.Stage.Digest = strings.Repeat("2", 64)
			case "clear":
				b.Workspace.Stage = nil
			case "forget-stopped":
				b.Workspace.Stage = nil
				b.Lease.Execution = nil
			}
			if err := ValidateTransition(before, rows); err == nil {
				t.Fatal("durable stage identity changed or was forgotten")
			}
		})
	}
	b := aggregate()
	b.Workspace.Phase = "initializing"
	b.Lease.Execution = trackedExecution()
	b.Lease.Execution.Phase = "stopped"
	b.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("1", 64)}
	rows := Database{"card": b}
	changeValid(t, rows, func(b *model.Bench) {
		b.Lease.RetainedRuns = append(b.Lease.RetainedRuns, b.Lease.Execution.ID)
		b.Lease.Execution = nil
		b.Workspace.Stage = nil
	})
}

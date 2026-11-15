package benchstate

import (
	"encoding/json"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

func archiveIntent() *model.WorkspaceArchive {
	return &model.WorkspaceArchive{ArchivedAt: "2030-01-01T00:00:00Z", ReapAfter: "2030-01-08T00:00:00Z", FreezeDigest: strings.Repeat("a", 64)}
}
func TestAggregateArchiveTransitionsPreserveClosedIdentityAndAdvanceIntent(t *testing.T) {
	b := readyAggregate(t)
	rows := Database{"card": b}
	changeValid(t, rows, func(b *model.Bench) { b.Workspace.Archive = archiveIntent(); b.Workspace.Phase = "archived" })
	changeValid(t, rows, func(b *model.Bench) { b.Workspace.Archive.FrozenAt = "2030-01-01T00:01:00Z" })
	changeValid(t, rows, func(b *model.Bench) {
		b.Workspace.Archive.ReapingDigest = strings.Repeat("b", 64)
		b.Workspace.Phase = "reaping"
	})
	changeValid(t, rows, func(b *model.Bench) {
		b.Workspace.Archive.ReapedAt = "2030-01-08T00:01:00Z"
		b.Workspace.Phase = "reaped"
	})
}
func TestAggregateArchiveCannotReplaceOrForgetClosedIntent(t *testing.T) {
	changes := map[string]func(*model.Bench){
		"remove":     func(b *model.Bench) { b.Workspace.Archive = nil; b.Workspace.Phase = "ready" },
		"time":       func(b *model.Bench) { b.Workspace.Archive.ArchivedAt = "2030-01-01T00:00:01Z" },
		"deadline":   func(b *model.Bench) { b.Workspace.Archive.ReapAfter = "2030-01-09T00:00:00Z" },
		"freeze":     func(b *model.Bench) { b.Workspace.Archive.FreezeDigest = strings.Repeat("b", 64) },
		"checkpoint": func(b *model.Bench) { b.Workspace.Checkpoint.Seal = strings.Repeat("b", 40) },
		"prep":       func(b *model.Bench) { b.Workspace.Preparation[0].Digest = strings.Repeat("b", 64) },
		"quota":      func(b *model.Bench) { b.ProjectID = 10001; b.QuotaBytes = 4096; b.QuotaInodes = 128 },
		"unmerged":   func(b *model.Bench) { b.Workspace.Unmerged = []model.WorkspaceMergeEvidence{{Name: "api"}} },
		"merge":      func(b *model.Bench) { b.Workspace.Archive.Merge = []model.WorkspaceMergeEvidence{{Name: "api"}} },
		"skip": func(b *model.Bench) {
			b.Workspace.Phase = "reaped"
			b.Workspace.Archive.FrozenAt = "2030-01-01T00:01:00Z"
			b.Workspace.Archive.ReapingDigest = strings.Repeat("b", 64)
			b.Workspace.Archive.ReapedAt = "2030-01-08T00:01:00Z"
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			b := readyAggregate(t)
			b.Workspace.Archive = archiveIntent()
			b.Workspace.Phase = "archived"
			rows := Database{"card": b}
			before := Capture(rows)
			change(b)
			if err := ValidateTransition(before, rows); err == nil {
				t.Fatal("closed archive identity changed")
			}
		})
	}
	b := readyAggregate(t)
	b.Workspace.Archive = archiveIntent()
	b.Workspace.Archive.FrozenAt = "2030-01-01T00:01:00Z"
	b.Workspace.Phase = "archived"
	rows := Database{"card": b}
	before := Capture(rows)
	b.Workspace.Archive.FrozenAt = ""
	if err := ValidateTransition(before, rows); err == nil {
		t.Fatal("freeze completion cleared")
	}
}
func TestUnmergedObservationChangesOnlyWithReadyHeldCheckpointCompletion(t *testing.T) {
	b := readyAggregate(t)
	b.Lease = operationalLease()
	b.Workspace.Preparation = nil
	rows := Database{"card": b}
	before := Capture(rows)
	b.Workspace.Unmerged = []model.WorkspaceMergeEvidence{{Name: "api"}}
	if err := ValidateTransition(before, rows); err == nil {
		t.Fatal("observation replaced without checkpoint")
	}
	b.Workspace.Unmerged = nil
	b.Workspace.Pending = pendingCheckpoint(t, b)
	b.Workspace.Pending.GenerationID = strings.Repeat("8", 32)
	b.Workspace.Pending.Members[0].Commit = strings.Repeat("7", 40)
	m, err := workspacepolicy.Manifest(b, b.Workspace.Pending)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	b.Workspace.Pending.Digest = checkpointset.Digest(raw)
	if err := Validate(rows); err != nil {
		t.Fatal(err)
	}
	before = Capture(rows)
	b.Workspace.Checkpoint = copyCheckpoint(b.Workspace.Pending)
	b.Workspace.Checkpoint.Seal = strings.Repeat("9", 40)
	b.Workspace.Pending = nil
	b.Workspace.Unmerged = []model.WorkspaceMergeEvidence{{Name: "api", Evidence: model.ArchiveMergeEvidence{Checkpoint: strings.Repeat("7", 40), ConfigDigest: strings.Repeat("a", 64), TargetBranch: "refs/heads/main", TargetCommit: strings.Repeat("b", 40), ObservedAt: "2030-01-01T00:00:00Z"}}}
	if err := Validate(rows); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTransition(before, rows); err != nil {
		t.Fatal(err)
	}
}
func TestSchema17UpgradePreservesStatesAndRejectsArchiveFields(t *testing.T) {
	b := readyAggregate(t)
	db := oldDatabase(t, 17, b)
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
	if string(before) != string(after) {
		t.Fatal("upgrade altered existing state")
	}
	for _, version := range []int{14, 15, 16, 17} {
		b := readyAggregate(t)
		b.Workspace.Archive = archiveIntent()
		b.Workspace.Phase = "archived"
		db := oldDatabase(t, version, b)
		if _, err := LoadKinds(db, t.TempDir()); err == nil {
			t.Fatal("historical kind preflight adopted archive", version)
		}
		if err := migrateForTest(db, t.TempDir(), nil); err == nil {
			t.Fatal("historical migration adopted archive", version)
		}
		assertVersion(t, db, version)
	}
	b = readyAggregate(t)
	b.Lease = operationalLease()
	b.Lease.Execution = trackedExecution()
	db = oldDatabase(t, 17, b)
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("active supervisor upgraded")
	}
	assertVersion(t, db, 17)
}

func TestArchiveSnapshotOwnsBothObservationVectors(t *testing.T) {
	for _, field := range []string{"merge", "unmerged"} {
		t.Run(field, func(t *testing.T) {
			b := readyAggregate(t)
			b.Workspace.Checkpoint.Members[0].Commit = strings.Repeat("7", 40)
			manifest, err := workspacepolicy.Manifest(b, b.Workspace.Checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(manifest)
			b.Workspace.Checkpoint.Digest = checkpointset.Digest(raw)
			e := model.WorkspaceMergeEvidence{Name: "api", Evidence: model.ArchiveMergeEvidence{Checkpoint: strings.Repeat("7", 40), ConfigDigest: strings.Repeat("a", 64), TargetBranch: "refs/heads/main", TargetCommit: strings.Repeat("b", 40), ObservedAt: "2029-12-30T00:00:00Z"}}
			b.Workspace.Unmerged = []model.WorkspaceMergeEvidence{e}
			e.Evidence.ObservedAt = "2029-12-31T00:00:00Z"
			b.Workspace.Archive = archiveIntent()
			b.Workspace.Archive.Merge = []model.WorkspaceMergeEvidence{e}
			b.Workspace.Phase = "archived"
			rows := Database{"card": b}
			if err := Validate(rows); err != nil {
				t.Fatal(err)
			}
			before := Capture(rows)
			if field == "merge" {
				b.Workspace.Archive.Merge[0].Evidence.TargetCommit = strings.Repeat("c", 40)
			} else {
				b.Workspace.Unmerged[0].Evidence.TargetCommit = strings.Repeat("c", 40)
			}
			if err := ValidateTransition(before, rows); err == nil {
				t.Fatal("snapshot aliased mutable archive observations")
			}
		})
	}
}

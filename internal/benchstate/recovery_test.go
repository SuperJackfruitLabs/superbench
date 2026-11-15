package benchstate

import (
	"encoding/json"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func recoveredAggregate() *model.Bench {
	b := aggregate()
	b.Workspace.RecoveredFrom = &model.WorkspaceRecovery{Repository: b.Workspace.Members[0].Repository, BenchID: strings.Repeat("1", 32), Commit: strings.Repeat("2", 40), ManifestSHA256: strings.Repeat("3", 64)}
	return b
}
func TestRecoveryIdentityIsImmutableAndSnapshotOwnsProvenance(t *testing.T) {
	for _, field := range []string{"repo", "id", "commit", "digest", "remove", "add"} {
		t.Run(field, func(t *testing.T) {
			b := recoveredAggregate()
			if field == "add" {
				b.Workspace.RecoveredFrom = nil
			}
			rows := Database{"card": b}
			before := Capture(rows)
			switch field {
			case "repo":
				b.Workspace.RecoveredFrom.Repository = "/srv/other"
			case "id":
				b.Workspace.RecoveredFrom.BenchID = strings.Repeat("4", 32)
			case "commit":
				b.Workspace.RecoveredFrom.Commit = strings.Repeat("5", 40)
			case "digest":
				b.Workspace.RecoveredFrom.ManifestSHA256 = strings.Repeat("6", 64)
			case "remove":
				b.Workspace.RecoveredFrom = nil
			case "add":
				b.Workspace.RecoveredFrom = recoveredAggregate().Workspace.RecoveredFrom
			}
			if err := ValidateTransition(before, rows); err == nil {
				t.Fatal("recovery identity was rebound")
			}
		})
	}
	rows := Database{"card": recoveredAggregate()}
	changeValid(t, rows, func(b *model.Bench) { b.Workspace.Phase = "initializing" })
}
func TestSchema16UpgradePreservesIdentityAndPartialReadyState(t *testing.T) {
	b := readyAggregate(t)
	b.Workspace.Preparation = nil
	db := oldDatabase(t, 16, b)
	before, _ := json.Marshal(b)
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, db, 22)
	rows, err := Load(db)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(rows["card"])
	if string(before) != string(after) {
		t.Fatal("upgrade changed existing identity or checkpoint")
	}
	if err := CheckVersion(db); err != nil {
		t.Fatal(err)
	}
}
func TestRecoveryProvenanceCannotBeAdoptedByOlderSchemaMigrationOrKindRead(t *testing.T) {
	for _, version := range []int{14, 15, 16} {
		db := oldDatabase(t, version, recoveredAggregate())
		if _, err := LoadKinds(db, t.TempDir()); err == nil {
			t.Fatal("kind preflight accepted provenance mislabeled as historical schema", version)
		}
		if err := migrateForTest(db, t.TempDir(), nil); err == nil {
			t.Fatal("migration adopted provenance from historical schema", version)
		}
		assertVersion(t, db, version)
	}
}
func TestSchema16UpgradeStillRefusesActiveSupervisor(t *testing.T) {
	b := readyAggregate(t)
	b.Workspace.Preparation = nil
	b.Lease = operationalLease()
	b.Lease.Execution = trackedExecution()
	db := oldDatabase(t, 16, b)
	if err := migrateForTest(db, t.TempDir(), nil); err == nil {
		t.Fatal("upgrade ignored active supervisor")
	}
	assertVersion(t, db, 16)
}

package benchstate

import (
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func outgoing(t *testing.T) *model.Bench {
	b := readyAggregate(t)
	b.Workspace.Transfer = &model.WorkspaceTransfer{ID: "transfer1", Digest: strings.Repeat("1", 64), TargetKey: "target", TargetID: strings.Repeat("e", 32), CreationID: "transfer-create", Phase: "relinquishing"}
	return b
}
func incoming(source *model.Bench) *model.Bench {
	s := source.Workspace
	t := s.Transfer
	path := filepath.Join(filepath.Dir(source.Path), t.TargetKey)
	b := &model.Bench{Key: t.TargetKey, Path: path, RequestedQuotaBytes: source.RequestedQuotaBytes, Lease: &model.Lease{Token: strings.Repeat("6", 48), Continuation: strings.Repeat("7", 32), Holder: "initialization"}, Workspace: &model.Workspace{ID: t.TargetID, CreationID: t.CreationID, CheckpointRepository: s.CheckpointRepository, ExplicitQuotaBytes: s.ExplicitQuotaBytes, Phase: "materializing", TransferredFrom: &model.WorkspaceTransferOrigin{ID: t.ID, Digest: t.Digest, SourceKey: source.Key, SourceID: s.ID}}}
	for i, m := range s.Members {
		m.Path = filepath.Join(path, m.Name)
		m.Source = s.Checkpoint.Members[i].Commit
		b.Workspace.Members = append(b.Workspace.Members, m)
	}
	return b
}
func transferValid(t *testing.T, before IdentitySnapshot, after Database) {
	t.Helper()
	if err := Validate(after); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTransition(before, after); err != nil {
		t.Fatal(err)
	}
}

func TestTransferSourceRelinquishesBeforeDestinationAndNeverRegainsWriter(t *testing.T) {
	source := readyAggregate(t)
	rows := Database{"card": source}
	before := Capture(rows)
	source.Workspace.Transfer = outgoing(t).Workspace.Transfer
	transferValid(t, before, rows)
	before = Capture(rows)
	source.Workspace.Transfer.Phase = "transferred"
	transferValid(t, before, rows)
	// Source metadata can archive before destination materialization; the seal and
	// original branch inventory remain available to prove its incoming identity.
	before = Capture(rows)
	source.Workspace.Archive = archiveIntent()
	source.Workspace.Phase = "archived"
	transferValid(t, before, rows)
	before = Capture(rows)
	target := incoming(source)
	rows[target.Key] = target
	transferValid(t, before, rows)
	before = Capture(rows)
	target.Workspace.Phase = "initializing"
	transferValid(t, before, rows)
	before = Capture(rows)
	target.Workspace.Preparation = []model.WorkspacePreparation{{Name: "api", Digest: strings.Repeat("2", 64)}}
	target.Workspace.Phase = "prepared"
	transferValid(t, before, rows)
	before = Capture(rows)
	target.Workspace.Pending = pendingCheckpoint(t, target)
	transferValid(t, before, rows)
	before = Capture(rows)
	target.Workspace.Checkpoint = copyCheckpoint(target.Workspace.Pending)
	target.Workspace.Checkpoint.Seal = strings.Repeat("f", 40)
	target.Workspace.Pending = nil
	target.Workspace.Phase = "ready"
	target.Lease = nil
	transferValid(t, before, rows)
	before = Capture(rows)
	target.Lease = operationalLease()
	target.Workspace.Preparation = nil
	transferValid(t, before, rows)
	// The source never gets a writer back, even though its initialization phase
	// may remain ready in the ordinary source-without-archive case.
	readySource := outgoing(t)
	readySource.Workspace.Transfer.Phase = "transferred"
	old := Capture(Database{"card": readySource})
	readySource.Lease = operationalLease()
	readySource.Workspace.Preparation = nil
	if err := ValidateTransition(old, Database{"card": readySource}); err == nil {
		t.Fatal("transferred source regained writer authority")
	}
}

func TestTransferStartAndProgressCannotChangeSourceAuthority(t *testing.T) {
	changes := map[string]func(*model.Bench){
		"skip-relinquishing":          func(b *model.Bench) { b.Workspace.Transfer.Phase = "transferred" },
		"held-owner":                  func(b *model.Bench) { b.Lease = operationalLease(); b.Workspace.Preparation = nil },
		"change-checkpoint":           func(b *model.Bench) { b.Workspace.Checkpoint.Seal = strings.Repeat("a", 40) },
		"clear-preparation":           func(b *model.Bench) { b.Workspace.Preparation = nil },
		"quota-assignment":            func(b *model.Bench) { b.ProjectID = 4; b.QuotaBytes = 4096; b.QuotaInodes = 100 },
		"archive-while-relinquishing": func(b *model.Bench) { b.Workspace.Archive = archiveIntent(); b.Workspace.Phase = "archived" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			before := Capture(Database{"card": readyAggregate(t)})
			after := outgoing(t)
			change(after)
			if err := ValidateTransition(before, Database{"card": after}); err == nil {
				t.Fatal("transfer start bypassed source guard")
			}
		})
	}
	for _, kind := range []string{"remove", "rebind", "reverse", "change-quota"} {
		t.Run(kind, func(t *testing.T) {
			source := outgoing(t)
			source.Workspace.Transfer.Phase = "transferred"
			before := Capture(Database{"card": source})
			switch kind {
			case "remove":
				source.Workspace.Transfer = nil
			case "rebind":
				source.Workspace.Transfer.TargetID = strings.Repeat("a", 32)
			case "reverse":
				source.Workspace.Transfer.Phase = "relinquishing"
			case "change-quota":
				source.QuotaInodes++
			}
			if err := ValidateTransition(before, Database{"card": source}); err == nil {
				t.Fatal("irreversible source transfer changed")
			}
		})
	}
	source := readyAggregate(t)
	before := Capture(Database{"card": source, "target": aggregate()})
	source.Workspace.Transfer = outgoing(t).Workspace.Transfer
	if err := ValidateTransition(before, Database{"card": source, "target": aggregate()}); err == nil {
		t.Fatal("transfer reserved existing destination key")
	}
}

func TestIncomingTransferRequiresExactRelinquishedSourceAndInitialVector(t *testing.T) {
	for _, kind := range []string{"missing-source", "not-relinquished", "source-id", "digest", "target-id", "creation", "task-ref", "commit", "existing-target", "born-ready"} {
		t.Run(kind, func(t *testing.T) {
			source := outgoing(t)
			source.Workspace.Transfer.Phase = "transferred"
			target := incoming(source)
			before := Capture(Database{"card": source})
			rows := Database{"card": source, "target": target}
			switch kind {
			case "missing-source":
				delete(rows, "card")
			case "not-relinquished":
				source.Workspace.Transfer.Phase = "relinquishing"
			case "source-id":
				target.Workspace.TransferredFrom.SourceID = strings.Repeat("a", 32)
			case "digest":
				target.Workspace.TransferredFrom.Digest = strings.Repeat("a", 64)
			case "target-id":
				target.Workspace.ID = strings.Repeat("a", 32)
			case "creation":
				target.Workspace.CreationID = "different"
			case "task-ref":
				target.Workspace.Members[0].Branch = "bench/target"
			case "commit":
				target.Workspace.Members[0].Source = strings.Repeat("a", 40)
			case "existing-target":
				existing := incoming(source)
				existing.Workspace.TransferredFrom = nil
				before = Capture(Database{"card": source, "target": existing})
			case "born-ready":
				target.Workspace.Phase = "ready"
				target.Lease = nil
				target.Workspace.Checkpoint = source.Workspace.Checkpoint
			}
			if err := ValidateTransition(before, rows); err == nil {
				t.Fatal("incoming origin fabricated handoff authority")
			}
		})
	}
	first := outgoing(t)
	second := outgoing(t)
	second.Key = "second"
	second.Path = filepath.Join(filepath.Dir(second.Path), second.Key)
	second.Workspace.ID = strings.Repeat("a", 32)
	second.Workspace.Transfer.ID = "transfer2"
	if err := validateTransferLinks(Database{"card": first, "second": second}); err == nil {
		t.Fatal("competing outgoing transfers reserved same target")
	}
}

func TestTransferSnapshotsDeepCopyBothDirections(t *testing.T) {
	source := outgoing(t)
	source.Workspace.Transfer.Phase = "transferred"
	target := incoming(source)
	before := Capture(Database{"card": source, "target": target})
	source.Workspace.Transfer.Digest = strings.Repeat("a", 64)
	target.Workspace.TransferredFrom.Digest = strings.Repeat("a", 64)
	if before["card"].transfer.Digest == source.Workspace.Transfer.Digest || before["target"].workspace.TransferredFrom.Digest == target.Workspace.TransferredFrom.Digest {
		t.Fatal("snapshot aliased transfer authority")
	}
	if err := ValidateTransition(before, Database{"card": source, "target": target}); err == nil {
		t.Fatal("matching mutation rewrote immutable links")
	}
}

func TestTransferReservationCannotCollideWithOtherWorkspaceIdentity(t *testing.T) {
	source := outgoing(t)
	other := readyAggregate(t)
	other.Key = "other"
	other.Workspace.ID = source.Workspace.Transfer.TargetID
	if err := validateTransferLinks(Database{source.Key: source, other.Key: other}); err == nil {
		t.Fatal("target identity already belongs to another key")
	}
	other.Workspace.ID = strings.Repeat("9", 32)
	second := *source
	w := *source.Workspace
	marker := *source.Workspace.Transfer
	second.Key = "second"
	second.Workspace = &w
	w.Transfer = &marker
	marker.ID = "transfer2"
	marker.TargetKey = "target2"
	if err := validateTransferLinks(Database{source.Key: source, second.Key: &second}); err == nil {
		t.Fatal("two reservations share target identity")
	}
}

package benchstate

import (
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func missingRetirement(t *testing.T, held bool) (*model.Bench, *model.Bench) {
	t.Helper()
	old := readyAggregate(t)
	if held {
		old.Lease = operationalLease()
		old.Lease.StopRequested = true
		old.Lease.Continuation = ""
		old.Lease.Execution = trackedExecution()
		old.Lease.Execution.Phase = "stopped"
		old.Lease.RetainedRuns = []string{strings.Repeat("1", 32)}
		old.Workspace.Preparation = []model.WorkspacePreparation{{Name: "api", Digest: strings.Repeat("2", 64), RunID: old.Lease.RetainedRuns[0]}}
		old.Workspace.Stage = &model.WorkspaceStage{Name: "api", Digest: strings.Repeat("3", 64)}
	}
	next := readyAggregate(t)
	next.Workspace.Preparation = append([]model.WorkspacePreparation(nil), old.Workspace.Preparation...)
	next.Workspace.Transfer = outgoing(t).Workspace.Transfer
	snapshot := Capture(Database{old.Key: old})
	r := &model.WorkspaceTransferRecovery{Mode: "missing", SourceRowDigest: snapshot[old.Key].rowDigest, RetiredAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if held {
		r.Holder = old.Lease.Holder
		e := *old.Lease.Execution
		r.Execution = &e
		s := *old.Workspace.Stage
		r.Stage = &s
		for _, id := range []string{old.Lease.RetainedRuns[0], old.Lease.Execution.ID} {
			r.Runs = append(r.Runs, model.WorkspaceRetiredRun{ID: id, Digest: strings.Repeat("4", 64)})
		}
		if r.Runs[0].ID > r.Runs[1].ID {
			r.Runs[0], r.Runs[1] = r.Runs[1], r.Runs[0]
		}
	}
	next.Workspace.Transfer.Recovery = r
	return old, next
}
func TestMissingTransferRetiresExactStoppedOwnerWithoutReplacingCheckpointOrProof(t *testing.T) {
	for _, held := range []bool{false, true} {
		old, next := missingRetirement(t, held)
		if err := Validate(Database{old.Key: old}); err != nil {
			t.Fatal(err)
		}
		transferValid(t, Capture(Database{old.Key: old}), Database{next.Key: next})
		prior := Capture(Database{next.Key: next})
		next.Workspace.Transfer.Phase = "transferred"
		if err := ValidateTransition(prior, Database{next.Key: next}); err != nil {
			t.Fatal(err)
		}
		prior = Capture(Database{next.Key: next})
		next.Workspace.Transfer.Recovery.Execution = nil
		if held && ValidateTransition(prior, Database{next.Key: next}) == nil {
			t.Fatal("retired scope evidence removed")
		}
	}
}
func TestMissingRetirementCannotBypassStoppedOwnerOrLoseDependencies(t *testing.T) {
	for name, change := range map[string]func(*model.Bench, *model.Bench){
		"not-stopped":       func(o, n *model.Bench) { o.Lease.StopRequested = false },
		"live-continuation": func(o, n *model.Bench) { o.Lease.Continuation = strings.Repeat("6", 32) },
		"running": func(o, n *model.Bench) {
			o.Lease.Execution.Phase = "running"
			n.Workspace.Transfer.Recovery.Execution.Phase = "running"
		},
		"different-owner": func(o, n *model.Bench) { n.Workspace.Transfer.Recovery.Holder = "another" },
		"changed-scope":   func(o, n *model.Bench) { n.Workspace.Transfer.Recovery.Execution.HostID = "other-host" },
		"lost-stage":      func(o, n *model.Bench) { n.Workspace.Transfer.Recovery.Stage = nil },
		"lost-run":        func(o, n *model.Bench) { n.Workspace.Transfer.Recovery.Runs = n.Workspace.Transfer.Recovery.Runs[:1] },
		"new-run":         func(o, n *model.Bench) { n.Workspace.Transfer.Recovery.Runs[0].ID = strings.Repeat("0", 32) },
		"pending": func(o, n *model.Bench) {
			o.Workspace.Pending = pendingCheckpoint(t, o)
			n.Workspace.Pending = o.Workspace.Pending
		},
		"changed-checkpoint": func(o, n *model.Bench) { n.Workspace.Checkpoint.Seal = strings.Repeat("9", 40) },
		"dropped-proof":      func(o, n *model.Bench) { n.Workspace.Preparation = nil },
		"wrong-private-row":  func(o, n *model.Bench) { n.Workspace.Transfer.Recovery.SourceRowDigest = strings.Repeat("0", 64) },
		"kept-live-owner":    func(o, n *model.Bench) { n.Lease = copyLease(o.Lease) },
	} {
		t.Run(name, func(t *testing.T) {
			old, next := missingRetirement(t, true)
			change(old, next)
			if err := ValidateTransition(Capture(Database{old.Key: old}), Database{next.Key: next}); err == nil {
				t.Fatal("unsafe retirement accepted")
			}
		})
	}
}
func TestRetirementSnapshotDeepCopiesScopeStageAndRuns(t *testing.T) {
	_, b := missingRetirement(t, true)
	before := Capture(Database{b.Key: b})
	b.Workspace.Transfer.Recovery.Execution.HostID = "changed"
	b.Workspace.Transfer.Recovery.Stage.Digest = strings.Repeat("9", 64)
	b.Workspace.Transfer.Recovery.Runs[0].Digest = strings.Repeat("8", 64)
	if before[b.Key].transfer.Recovery.Execution.HostID == "changed" || before[b.Key].transfer.Recovery.Stage.Digest == strings.Repeat("9", 64) || before[b.Key].transfer.Recovery.Runs[0].Digest == strings.Repeat("8", 64) {
		t.Fatal("snapshot aliases retirement metadata")
	}
	if ValidateTransition(before, Database{b.Key: b}) == nil {
		t.Fatal("retirement evidence mutated")
	}
}

func TestRetirementEmptyRunVectorRetainsExactSnapshotIdentity(t *testing.T) {
	_, b := missingRetirement(t, false)
	b.Workspace.Transfer.Recovery.Runs = []model.WorkspaceRetiredRun{}
	before := Capture(Database{b.Key: b})
	if before[b.Key].transfer.Recovery.Runs == nil {
		t.Fatal("snapshot normalized empty approved vector to null")
	}
	if err := ValidateTransition(before, Database{b.Key: b}); err != nil {
		t.Fatal("unchanged persisted empty vector rejected", err)
	}
	b.Workspace.Transfer.Phase = "transferred"
	if err := ValidateTransition(before, Database{b.Key: b}); err != nil {
		t.Fatal("empty vector blocked durable relinquishment", err)
	}
}

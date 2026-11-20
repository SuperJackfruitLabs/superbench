package workspacepolicy_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// unfinished is a second generation whose create never completed: its first
// record, materializing, owned only by its initialization lease.
func unfinished(t *testing.T) *model.Bench {
	b := successor(reclaimed(t))
	if !workspacepolicy.Unfinished(b) {
		t.Fatal("a materializing record is not unfinished")
	}
	return b
}

// nextOf is the record a create on old's key commits first, with its own ID.
func nextOf(old *model.Bench) *model.Bench {
	b := successor(old)
	b.Workspace.ID = strings.Repeat("6", 32)
	return b
}

// A new create follows an unfinished create only on root's proof, asked
// before any plan, with the exact record; never on the daemon's record alone.
func TestAnUnfinishedCreateIsFollowedOnlyOnRootsProof(t *testing.T) {
	ctx := context.Background()
	old := unfinished(t)
	var asked []*model.Bench
	proven := func(_ context.Context, b *model.Bench) error { asked = append(asked, workspace.Clone(b)); return nil }
	denied := func(context.Context, *model.Bench) error { return errors.New("root: not retired") }
	for name, c := range map[string]struct {
		previous *model.Bench
		fresh    bool
		retired  func(context.Context, *model.Bench) error
	}{
		"no proof route": {old, true, nil},
		"root refuses":   {old, true, denied},
		"not fresh":      {old, false, proven},
		"a ready bench":  {workspacepolicy.ReleasedRecord(t), true, proven},
	} {
		asked = nil
		proof, err := workspacepolicy.Prove(ctx, c.previous, c.fresh, c.retired, nil)
		if err == nil || proof != (workspacepolicy.Proof{}) || name != "root refuses" && len(asked) != 0 {
			t.Errorf("%s: proven %v, root asked %d times", name, err, len(asked))
		}
		if name != "not fresh" && !errors.Is(err, workspacepolicy.ErrUnretired) {
			t.Errorf("%s: %v is not ErrUnretired", name, err)
		}
	}
	asked = nil
	if proof, err := workspacepolicy.Prove(ctx, reclaimed(t), true, proven, nil); err != nil || proof == (workspacepolicy.Proof{}) || len(asked) != 0 {
		t.Fatalf("a reclaimed record needs no root proof: %v %d", err, len(asked))
	}
	if proof, err := workspacepolicy.Prove(ctx, nil, false, nil, nil); err != nil || proof.Follow(old) != nil {
		t.Fatalf("a first create: %v", err)
	}
	proof, err := workspacepolicy.Prove(ctx, old, true, proven, nil)
	if err != nil || len(asked) != 1 || asked[0].Workspace.ID != old.Workspace.ID {
		t.Fatalf("proof: %v %d", err, len(asked))
	}
	next := nextOf(old)
	next.Workspace.Generation, next.Workspace.Previous = 0, ""
	if err = proof.Follow(next); err != nil || next.Workspace.Generation != 3 || next.Workspace.Previous != old.Workspace.ID {
		t.Fatalf("follow: %v %+v", err, next.Workspace)
	}
	other := nextOf(old)
	other.Key = "other"
	if proof.Follow(other) == nil {
		t.Fatal("a record under another key followed")
	}
	if workspacepolicy.Follow(nextOf(old), old) == nil {
		t.Fatal("Follow took an unfinished record without root's proof")
	}
	t.Log("FAILED_CREATE_DAEMON_FOLLOWS_ONLY_ON_ROOT_PROOF_PASS")
}

// State admits an unfinished record giving way only to its next generation,
// exactly as a reclaimed one does.
func TestAnUnfinishedRecordGivesWayOnlyToItsNextGeneration(t *testing.T) {
	step := func(from, to *model.Bench) error {
		if err := benchstate.Validate(benchstate.Database{"card": to}); err != nil {
			return err
		}
		return benchstate.ValidateTransition(benchstate.Capture(benchstate.Database{"card": from}), benchstate.Database{"card": to})
	}
	old := unfinished(t)
	if err := step(old, nextOf(old)); err != nil {
		t.Fatal("next generation of an unfinished create", err)
	}
	for name, c := range map[string]struct {
		from   func(*model.Bench)
		change func(*model.Bench)
	}{
		"skips one":     {nil, func(b *model.Bench) { b.Workspace.Generation++ }},
		"names another": {nil, func(b *model.Bench) { b.Workspace.Previous = strings.Repeat("8", 32) }},
		"allocated":     {nil, func(b *model.Bench) { b.ProjectID, b.QuotaBytes, b.QuotaInodes = 10007, 4096, 128 }},
		"from a run": {func(b *model.Bench) {
			b.Workspace.Phase, b.ProjectID, b.QuotaBytes, b.QuotaInodes = "initializing", 10009, 4096, 128
			b.Workspace.Stage = &model.WorkspaceStage{Name: b.Workspace.Members[0].Name, Digest: strings.Repeat("7", 64)}
			b.Lease.Execution = &model.Execution{ID: strings.Repeat("7", 32)}
		}, nil},
	} {
		from := unfinished(t)
		if c.from != nil {
			c.from(from)
		}
		to := nextOf(from)
		if c.change != nil {
			c.change(to)
		}
		if err := step(from, to); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

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

// replacementOf is the record a create on old's key commits first when root
// proved old never allocated: its own ID, old's generation and predecessor.
func replacementOf(old *model.Bench) *model.Bench {
	b := nextOf(old)
	b.Workspace.Generation, b.Workspace.Previous = old.Workspace.Generation, old.Workspace.Previous
	b.Workspace.CreationID, b.Lease.Token = "request3", strings.Repeat("c", 48)
	return b
}

// Root's proof of absence is asked only of an unfinished create root has not
// retired, and makes the new create replace it in its own generation.
func TestAnUnfinishedCreateRootNeverAllocatedIsReplacedInItsGeneration(t *testing.T) {
	ctx := context.Background()
	old := unfinished(t)
	var asked []string
	notRetired := func(context.Context, *model.Bench) error {
		asked = append(asked, "retired")
		return errors.New("root: no intent")
	}
	retired := func(context.Context, *model.Bench) error { asked = append(asked, "retired"); return nil }
	absent := func(_ context.Context, b *model.Bench) error {
		asked = append(asked, "absent")
		if b.Workspace.ID != old.Workspace.ID {
			t.Fatalf("root asked about another record")
		}
		return nil
	}
	present := func(context.Context, *model.Bench) error {
		asked = append(asked, "absent")
		return errors.New("root: intent exists")
	}

	proof, err := workspacepolicy.Prove(ctx, old, true, notRetired, absent)
	if err != nil || strings.Join(asked, ",") != "retired,absent" {
		t.Fatalf("absence proof: %v %v", err, asked)
	}
	next := nextOf(old)
	next.Workspace.Generation, next.Workspace.Previous = 0, ""
	if err = proof.Follow(next); err != nil || next.Workspace.Generation != old.Workspace.Generation || next.Workspace.Previous != old.Workspace.Previous {
		t.Fatalf("replace: %v %+v", err, next.Workspace)
	}
	same := workspace.Clone(old)
	if proof.Follow(same) == nil {
		t.Fatal("the refused record replaced itself")
	}
	other := nextOf(old)
	other.Key = "other"
	if proof.Follow(other) == nil {
		t.Fatal("a record under another key replaced it")
	}
	t.Log("CAPACITY_REFUSED_CREATE_REPLACED_ON_ROOT_ABSENCE_PASS")

	// A retired create is followed, never replaced: absence is not asked.
	asked = nil
	if proof, err = workspacepolicy.Prove(ctx, old, true, retired, absent); err != nil || strings.Join(asked, ",") != "retired" {
		t.Fatalf("retired: %v %v", err, asked)
	}
	next = nextOf(old)
	if err = proof.Follow(next); err != nil || next.Workspace.Generation != old.Workspace.Generation+1 {
		t.Fatalf("follow after retirement: %v %+v", err, next.Workspace)
	}
	// Root refuses both, or has no absence route: the record stands.
	for name, ask := range map[string]func(context.Context, *model.Bench) error{"present": present, "no route": nil} {
		asked = nil
		if proof, err = workspacepolicy.Prove(ctx, old, true, notRetired, ask); !errors.Is(err, workspacepolicy.ErrUnretired) || proof != (workspacepolicy.Proof{}) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A stalled create holds a project: absence is never asked of it.
	stalled := workspace.Clone(old)
	stalled.Workspace.Phase, stalled.ProjectID, stalled.QuotaBytes, stalled.QuotaInodes = "initializing", 10009, 4096, 128
	stalled.Lease.StopRequested, stalled.Lease.Continuation = true, ""
	if !workspacepolicy.Stalled(stalled) {
		t.Fatal("the stalled case is not stalled")
	}
	asked = nil
	if _, err = workspacepolicy.Prove(ctx, stalled, true, notRetired, absent); !errors.Is(err, workspacepolicy.ErrUnretired) || strings.Join(asked, ",") != "retired" {
		t.Fatalf("stalled: %v %v", err, asked)
	}
}

// State admits an unfinished record without a project giving way to a
// same-generation replacement, and nothing looser.
func TestAnUnfinishedRecordGivesWayToItsReplacement(t *testing.T) {
	step := func(from, to *model.Bench) error {
		if err := benchstate.Validate(benchstate.Database{"card": to}); err != nil {
			return err
		}
		return benchstate.ValidateTransition(benchstate.Capture(benchstate.Database{"card": from}), benchstate.Database{"card": to})
	}
	old := unfinished(t)
	if err := step(old, replacementOf(old)); err != nil {
		t.Fatal("replacement of an unfinished create:", err)
	}
	first := workspace.Clone(old)
	first.Workspace.Generation, first.Workspace.Previous = 0, ""
	if err := step(first, replacementOf(first)); err != nil {
		t.Fatal("replacement of a first generation:", err)
	}
	for name, c := range map[string]struct {
		from   func(*model.Bench)
		change func(*model.Bench)
	}{
		"same identity":      {nil, func(b *model.Bench) { b.Workspace.ID = old.Workspace.ID }},
		"same request":       {nil, func(b *model.Bench) { b.Workspace.CreationID = old.Workspace.CreationID }},
		"same owner":         {nil, func(b *model.Bench) { b.Lease.Token = old.Lease.Token }},
		"another generation": {nil, func(b *model.Bench) { b.Workspace.Generation = 3 }},
		"names another":      {nil, func(b *model.Bench) { b.Workspace.Previous = strings.Repeat("8", 32) }},
		"allocated":          {nil, func(b *model.Bench) { b.ProjectID, b.QuotaBytes, b.QuotaInodes = 10007, 4096, 128 }},
		"initializing":       {nil, func(b *model.Bench) { b.Workspace.Phase = "initializing" }},
		"from a project":     {func(b *model.Bench) { b.ProjectID, b.QuotaBytes, b.QuotaInodes = 10009, 4096, 128 }, nil},
		"from stalled": {func(b *model.Bench) {
			b.Workspace.Phase, b.ProjectID, b.QuotaBytes, b.QuotaInodes = "initializing", 10009, 4096, 128
			b.Lease.StopRequested, b.Lease.Continuation = true, ""
		}, nil},
		"from ready": {func(b *model.Bench) { *b = *workspacepolicy.ReleasedRecord(t) }, nil},
	} {
		from := unfinished(t)
		if c.from != nil {
			c.from(from)
		}
		to := replacementOf(from)
		if c.change != nil {
			c.change(to)
		}
		if err := step(from, to); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

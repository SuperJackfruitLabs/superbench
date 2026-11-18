package workspacepolicy_test

import (
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

func reclaimed(t *testing.T) *model.Bench {
	b := workspacepolicy.ReleasedRecord(t)
	b.Workspace.Phase = "reclaimed"
	return b
}

// successor is the record a create on old's key commits first.
func successor(old *model.Bench) *model.Bench {
	b := workspace.Clone(old)
	w := b.Workspace
	w.ID, w.CreationID, w.Phase, w.Checkpoint, w.Preparation = strings.Repeat("9", 32), "request2", "materializing", nil, nil
	w.Generation, w.Previous = workspacepolicy.NextGeneration(old), old.Workspace.ID
	b.ProjectID, b.QuotaBytes, b.QuotaInodes, b.Workspace.IdleSince = 0, 0, 0, ""
	b.Lease = &model.Lease{Holder: "initialization", Token: strings.Repeat("a", 48), Continuation: strings.Repeat("b", 32)}
	return b
}

// Only a reclaimed record gives way, and only to a new create of its next
// generation that names it and holds nothing yet.
func TestAReclaimedRecordGivesWayOnlyToItsNextGeneration(t *testing.T) {
	step := func(from, to *model.Bench) error {
		if err := benchstate.Validate(benchstate.Database{"card": to}); err != nil {
			return err
		}
		return benchstate.ValidateTransition(benchstate.Capture(benchstate.Database{"card": from}), benchstate.Database{"card": to})
	}
	old := reclaimed(t)
	next := successor(old)
	if err := step(old, next); err != nil || next.Workspace.Generation != 2 {
		t.Fatalf("second generation: %v", err)
	}
	// The second generation's own reclaim is followed by a third.
	second := workspace.Clone(next)
	second.Workspace.Phase, second.Workspace.Checkpoint, second.Lease = "reclaimed", workspacepolicy.ReleasedRecord(t).Workspace.Checkpoint, nil
	second.ProjectID, second.QuotaBytes, second.QuotaInodes = 10008, 4096, 128
	third := successor(second)
	if third.Workspace.Generation != 3 || third.Workspace.Previous != next.Workspace.ID {
		t.Fatalf("third generation %d %s", third.Workspace.Generation, third.Workspace.Previous)
	}
	for name, c := range map[string]struct {
		from   func() *model.Bench
		change func(*model.Bench)
	}{
		"from ready":      {func() *model.Bench { return workspacepolicy.ReleasedRecord(t) }, nil},
		"from reclaiming": {func() *model.Bench { b := reclaimed(t); b.Workspace.Phase = "reclaiming"; return b }, nil},
		"skips one":       {func() *model.Bench { return reclaimed(t) }, func(b *model.Bench) { b.Workspace.Generation = 3 }},
		"names another":   {func() *model.Bench { return reclaimed(t) }, func(b *model.Bench) { b.Workspace.Previous = strings.Repeat("8", 32) }},
		"same identity":   {func() *model.Bench { return reclaimed(t) }, func(b *model.Bench) { b.Workspace.ID = b.Workspace.Previous }},
		"allocated":       {func() *model.Bench { return reclaimed(t) }, func(b *model.Bench) { b.ProjectID, b.QuotaBytes, b.QuotaInodes = 10007, 4096, 128 }},
		"a writer":        {func() *model.Bench { return reclaimed(t) }, func(b *model.Bench) { b.Lease.Holder = "worker" }},
		"no owner":        {func() *model.Bench { return reclaimed(t) }, func(b *model.Bench) { b.Lease = nil }},
		"checkpointed":    {func() *model.Bench { return reclaimed(t) }, func(b *model.Bench) { b.Workspace.Checkpoint = workspacepolicy.ReleasedRecord(t).Workspace.Checkpoint }},
		"initializing":    {func() *model.Bench { return reclaimed(t) }, func(b *model.Bench) { b.Workspace.Phase = "initializing" }},
	} {
		from := c.from()
		to := successor(from)
		if c.change != nil {
			c.change(to)
		}
		if err := step(from, to); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestGenerationFieldsAreAbsentOrNameAnotherReclaimedBench(t *testing.T) {
	for name, change := range map[string]func(*model.Bench){
		"generation without previous": func(b *model.Bench) { b.Workspace.Generation = 2 },
		"previous without generation": func(b *model.Bench) { b.Workspace.Previous = strings.Repeat("8", 32) },
		"generation one":              func(b *model.Bench) { b.Workspace.Generation, b.Workspace.Previous = 1, strings.Repeat("8", 32) },
		"beyond the bound": func(b *model.Bench) {
			b.Workspace.Generation, b.Workspace.Previous = workspacepolicy.MaxGeneration+1, strings.Repeat("8", 32)
		},
		"previous not an identity": func(b *model.Bench) { b.Workspace.Generation, b.Workspace.Previous = 2, "card" },
		"previous is itself":       func(b *model.Bench) { b.Workspace.Generation, b.Workspace.Previous = 2, b.Workspace.ID },
	} {
		b := workspacepolicy.ReleasedRecord(t)
		change(b)
		if workspacepolicy.Validate(b) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	b := workspacepolicy.ReleasedRecord(t)
	b.Workspace.Generation, b.Workspace.Previous = workspacepolicy.MaxGeneration, strings.Repeat("8", 32)
	if err := workspacepolicy.Validate(b); err != nil {
		t.Fatal("the last generation refused", err)
	}
}

// Only a reclaimed record under the same key is followed.
func TestFollowTakesOnlyAReclaimedRecordUnderItsKey(t *testing.T) {
	old := reclaimed(t)
	next := successor(old)
	next.Workspace.Generation, next.Workspace.Previous = 0, ""
	if err := workspacepolicy.Follow(next, old); err != nil || next.Workspace.Generation != 2 || next.Workspace.Previous != old.Workspace.ID {
		t.Fatalf("follow: %v %+v", err, next.Workspace)
	}
	for name, change := range map[string]func(*model.Bench){
		"ready":     func(b *model.Bench) { b.Workspace.Phase = "ready" },
		"other key": func(b *model.Bench) { b.Key = "other" },
	} {
		prior, candidate := reclaimed(t), successor(old)
		change(prior)
		if workspacepolicy.Follow(candidate, prior) == nil {
			t.Errorf("%s followed", name)
		}
	}
}

package workspacereclaim

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

// The sweep's condition is decided in the reclaim's own transaction: a refusal
// records nothing and runs nothing, and a reclaiming record resumes without it.
func TestEligibleGatesOnlyANewReclaim(t *testing.T) {
	h := newHarness(t, released(t))
	stale := errors.New("released within the grace")
	var seen []string
	h.m.Eligible = func(b *model.Bench) error { seen = append(seen, b.Workspace.Phase); return stale }
	before := workspace.Clone(h.s.rows["card"])
	if _, err := h.m.Reclaim(context.Background(), "card"); !errors.Is(err, ErrConflict) || !errors.Is(err, stale) {
		t.Fatalf("ineligible reclaim: %v", err)
	}
	if !reflect.DeepEqual(h.s.rows["card"], before) || h.s.commits != 0 || len(h.calls) != 0 || !reflect.DeepEqual(seen, []string{"ready"}) {
		t.Fatalf("an ineligible reclaim recorded or ran: %v %d %v", h.calls, h.s.commits, seen)
	}
	h.m.Eligible = func(*model.Bench) error { return nil }
	h.helperErr = []error{errors.New("the bench is in use")}
	if _, err := h.m.Reclaim(context.Background(), "card"); err == nil || h.s.rows["card"].Workspace.Phase != "reclaiming" {
		t.Fatalf("refused helper: %v", err)
	}
	h.m.Eligible, seen, h.calls = func(b *model.Bench) error { seen = append(seen, b.Workspace.Phase); return stale }, nil, nil
	if got, err := h.m.Reclaim(context.Background(), "card"); err != nil || got.Workspace.Phase != "reclaimed" || len(seen) != 0 || !reflect.DeepEqual(h.calls, []string{"helper:reclaiming"}) {
		t.Fatalf("resume: %v %v %v", err, seen, h.calls)
	}
}

// A later generation's reclaim names its generation, so root binds it to that
// generation's own intent and controls.
func TestReclaimSpecificationCarriesTheGeneration(t *testing.T) {
	b := released(t)
	b.Workspace.Phase, b.Workspace.Generation, b.Workspace.Previous = "reclaiming", 2, "e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0"
	s, err := Specification(b)
	if err != nil || s.Version != payloadbootstrap.GenerationVersion || s.Generation != 2 || s.Previous != b.Workspace.Previous {
		t.Fatalf("%+v %v", s, err)
	}
}

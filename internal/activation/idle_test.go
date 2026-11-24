package activation

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The idle grace defaults to 24 hours only while fresh_payload is on; 0 turns
// the sweep off and an explicit value is whole hours.
func TestReclaimIdleDefaultsAndExplicitValues(t *testing.T) {
	fresh := `{"version":"superbench.activation.v1","fresh_payload":true,"cancellation":true`
	for raw, want := range map[string]time.Duration{
		fresh + `}`:                                                  24 * time.Hour,
		fresh + `,"fresh_reclaim_idle_hours":0}`:                     0,
		fresh + `,"fresh_reclaim_idle_hours":1}`:                     time.Hour,
		fresh + `,"fresh_reclaim_idle_hours":72}`:                    72 * time.Hour,
		fresh + `,"fresh_reclaim_idle_hours":8760}`:                  8760 * time.Hour,
		`{"version":"superbench.activation.v1"}`:                     0,
		`{"version":"superbench.activation.v1","cancellation":true}`: 0,
	} {
		s, err := Parse([]byte(raw))
		if err != nil || s.ReclaimIdle() != want {
			t.Errorf("%s: %v %v, want %v", raw, s.ReclaimIdle(), err, want)
		}
		if err = s.Check(func() error { return nil }, func() error { return nil }); err != nil {
			t.Errorf("%s: Check: %v", raw, err)
		}
	}
	s, _ := Parse([]byte(fresh + `,"fresh_reclaim_idle_hours":0}`))
	if !s.ReclaimIdleSet || !strings.Contains(s.String(), "fresh_reclaim_idle=0s") {
		t.Fatal(s, s.String())
	}
	s, _ = Parse([]byte(fresh + `}`))
	if s.ReclaimIdleSet || !strings.Contains(s.String(), "fresh_reclaim_idle=24h0m0s") {
		t.Fatal(s.String())
	}
}

func TestReclaimIdleRefusesEverythingButWholeHoursInRange(t *testing.T) {
	prefix := `{"version":"superbench.activation.v1","fresh_payload":true,"cancellation":true,"fresh_reclaim_idle_hours":`
	for _, value := range []string{`-1`, `8761`, `1.5`, `1e2`, `24.0`, `01`, `"24"`, `"24h"`, `true`, `null`, `{}`, `99999999999`} {
		if got, err := Parse([]byte(prefix + value + `}`)); !errors.Is(err, ErrActivation) || got != (Switches{}) {
			t.Errorf("%s: accepted %v %v", value, got, err)
		}
	}
	if _, err := Parse([]byte(prefix + `1,"fresh_reclaim_idle_hours":2}`)); !errors.Is(err, ErrActivation) {
		t.Errorf("duplicate accepted: %v", err)
	}
}

// Startup refuses a grace without fresh_payload, and an out-of-range value
// however the switches were built.
func TestCheckRefusesAnInvalidIdleGrace(t *testing.T) {
	ok := func() error { return nil }
	for name, c := range map[string]struct {
		s   Switches
		why string
	}{
		"grace without fresh":      {Switches{Cancellation: true, ReclaimIdleHours: 24, ReclaimIdleSet: true}, "fresh_reclaim_idle_hours requires fresh_payload"},
		"zero grace without fresh": {Switches{ReclaimIdleSet: true}, "fresh_reclaim_idle_hours requires fresh_payload"},
		"grace over a year":        {Switches{FreshPayload: true, Cancellation: true, ReclaimIdleHours: 8761, ReclaimIdleSet: true}, "must be 0 (off) or 1 to 8760"},
	} {
		if err := c.s.Check(ok, ok); !errors.Is(err, ErrActivation) || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if (Switches{ReclaimIdleSet: true}).Any() {
		t.Fatal("the idle grace alone counts as an activated path")
	}
}

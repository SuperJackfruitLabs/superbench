package workspaceops

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree/freshtreetest"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacereclaim"
)

// The legacy path is unchanged: reclaim refuses a legacy workspace as a
// conflict, and a registered Fresh workspace without an installed helper
// route refuses closed, both with no state change.
func TestReclaimRefusesLegacyAndUninstalledFreshWithoutEffects(t *testing.T) {
	m, r := fixture(t)
	create(t, m, r)
	reclaim := protocolv2.Request{Version: protocolv2.Version, Operation: "reclaim", Key: "card"}
	before := snapshot(t, m)
	if _, err := m.Execute(context.Background(), reclaim, 42); !errors.Is(err, ErrConflict) || !errors.Is(err, workspacereclaim.ErrLegacy) {
		t.Fatalf("legacy reclaim: %v", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, m)) {
		t.Fatal("a refused reclaim changed the legacy workspace")
	}
	b := before[0]
	freshtreetest.Register(t, m.Store.State, &b)
	if _, err := m.Execute(context.Background(), reclaim, 42); !errors.Is(err, freshtree.ErrUnavailable) {
		t.Fatalf("uninstalled Fresh reclaim: %v", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, m)) {
		t.Fatal("a refused reclaim changed the Fresh workspace")
	}
	if _, err := m.Execute(context.Background(), protocolv2.Request{Version: protocolv2.Version, Operation: "reclaim", Key: "other"}, 42); err == nil {
		t.Fatal("an unknown key reclaimed")
	}
}

// The sweep's route always carries its condition; without one it is invalid.
func TestReclaimIdleRequiresItsCondition(t *testing.T) {
	m, r := fixture(t)
	create(t, m, r)
	if _, err := m.ReclaimIdle(context.Background(), "card", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unconditioned idle reclaim: %v", err)
	}
}

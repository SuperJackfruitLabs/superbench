//go:build linux

package freshrecovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshliveness"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
)

func liveStub(busy bool) freshliveness.Liveness {
	return freshliveness.Liveness{
		Scopes:    func() ([]string, error) { return nil, nil },
		Events:    func(string) ([]byte, error) { return nil, nil },
		Members:   func(string) ([]int, error) { return nil, nil },
		Processes: func() ([]int, error) { return []int{7}, nil },
		Payload:   func(int) (bool, error) { return true, nil },
		Uses:      func(int) (bool, error) { return busy, nil },
	}
}

// A failed removal or retirement is relabelled payload activity only while
// something still uses the bench.
func TestActiveRelabelsOnlyWhileInUse(t *testing.T) {
	failure := errors.New("directory not empty")
	if err := (&reclaiming{live: liveStub(true)}).active(failure); !errors.Is(err, ErrReclaimPayloadActive) || !errors.Is(err, failure) {
		t.Fatalf("busy: %v", err)
	}
	if err := (&reclaiming{live: liveStub(false)}).active(failure); errors.Is(err, ErrReclaimPayloadActive) || !errors.Is(err, failure) {
		t.Fatalf("idle: %v", err)
	}
	if err := (&reclaiming{live: liveStub(true)}).active(nil); err != nil {
		t.Fatalf("success relabelled: %v", err)
	}
}

// A retry whose intent is already reclaiming may follow effects, even when
// it fails before its steps run.
func TestARetainedRetryThatFailsEarlySaysEffectsMayHaveOccurred(t *testing.T) {
	for phase, want := range map[string]bool{"reclaiming": true, "reclaimed": true, "completed": false} {
		r := payloadbootstrap.Reclamation{Inspection: func() (payloadbootstrap.Inspection, error) {
			return payloadbootstrap.Inspection{Phase: phase}, nil
		}}
		retained := false
		_, err := reclaimAction(context.Background(), r, payloadbootstrap.Spec{}, &retained)
		if err == nil || retained != want {
			t.Fatalf("%s: retained %v, %v", phase, retained, err)
		}
		if got := effects(err, retained).Error(); strings.Contains(got, "effects may have occurred; retry the same reclaim") != want {
			t.Fatalf("%s: %q", phase, got)
		}
	}
}

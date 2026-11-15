package operationcontrol

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestScopeRejectsEffectsAfterDurableBarrier(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked := false
	gate := func(fn func() error) error {
		if blocked {
			return ErrAuthority
		}
		return fn()
	}
	intent := Intent{Target: Target{UID: 3, RequestID: "request", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "run", Epoch: strings.Repeat("b", 32)}
	scoped, err := WithScope(ctx, intent, gate)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err = Effect(scoped, func() error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	blocked = true
	if err = Effect(scoped, func() error { calls++; return nil }); !errors.Is(err, ErrAuthority) || calls != 1 {
		t.Fatal("late effect crossed barrier", err, calls)
	}
	got, ok := FromContext(scoped)
	if !ok || got != intent {
		t.Fatal("lost epoch")
	}
	cancel()
	blocked = false
	if err = Effect(scoped, func() error { calls++; return nil }); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("canceled effect ran", err, calls)
	}
}
func TestScopeCannotReplaceEpochOrUseAbsentGate(t *testing.T) {
	i := Intent{Target: Target{RequestID: "id", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	if _, err := WithScope(context.Background(), i, nil); err == nil {
		t.Fatal("missing durable gate accepted")
	}
	ctx, err := WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	i.Epoch = strings.Repeat("c", 32)
	if _, err = WithScope(ctx, i, func(fn func() error) error { return fn() }); err == nil {
		t.Fatal("existing epoch overwritten")
	}
}

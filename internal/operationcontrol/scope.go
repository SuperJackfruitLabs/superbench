package operationcontrol

import (
	"context"
	"errors"
)

type scopeKey struct{}
type scope struct {
	intent Intent
	gate   func(func() error) error
}

// WithScope requires a durable admission gate supplied by the transport owner.
// Rebinding an existing context is forbidden, including to a newer epoch of the
// same token. No callback here acquires the host lock or waits for child exit.
func WithScope(ctx context.Context, i Intent, gate func(func() error) error) (context.Context, error) {
	if !i.Valid() || gate == nil {
		return nil, ErrAuthority
	}
	if _, ok := ctx.Value(scopeKey{}).(*scope); ok {
		return nil, ErrConflict
	}
	return context.WithValue(ctx, scopeKey{}, &scope{intent: i, gate: gate}), nil
}
func FromContext(ctx context.Context) (Intent, bool) {
	if ctx == nil {
		return Intent{}, false
	}
	s, ok := ctx.Value(scopeKey{}).(*scope)
	if !ok || s == nil {
		return Intent{}, false
	}
	return s.intent, true
}

// Effect linearizes one short effect with durable cancellation admission. The
// callback must not wait on another lock or a child; long-running work uses this
// only for its launch handshake, then waits using the cancelable context.
// Contexts without an operation scope retain their existing local behavior.
func Effect(ctx context.Context, fn func() error) error {
	if fn == nil {
		return errors.New("operation effect callback required")
	}
	if ctx == nil {
		return fn()
	}
	s, _ := ctx.Value(scopeKey{}).(*scope)
	if s == nil {
		return fn()
	}
	return s.gate(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn()
	})
}
func Check(ctx context.Context) error { return Effect(ctx, func() error { return nil }) }

// Value is Effect for short effects that return one value as well as an error.
func Value[T any](ctx context.Context, fn func() (T, error)) (value T, err error) {
	err = Effect(ctx, func() error { value, err = fn(); return err })
	return value, err
}

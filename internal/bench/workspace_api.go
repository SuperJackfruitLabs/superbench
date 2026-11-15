package bench

import (
	"context"
	"crypto/subtle"
	"errors"
)

var ErrScalar = errors.New("scalar bench requires the scalar API")

// WithWorkspace retains the shared state lock through a compatibility check and
// fn. As with WithLegacy, fn must not reenter a state mutation.
func (s Store) WithWorkspace(key string, allowMissing bool, fn func() error) error {
	return s.readKinds(func(rows database) error {
		b := rows[key]
		if b == nil && !allowMissing {
			return ErrNotFound
		}
		if b != nil && b.Workspace == nil {
			return ErrScalar
		}
		if fn != nil {
			return fn()
		}
		return nil
	})
}

// CheckWorkspaceOwner verifies a holder capability without exposing the row's
// private token. Backend operations independently repeat their ownership gates.
func (s Store) CheckWorkspaceOwner(key, token, holder string) error {
	return s.read(func(rows database) error {
		b := rows[key]
		if b == nil {
			return ErrNotFound
		}
		if b.Workspace == nil {
			return ErrScalar
		}
		if b.Lease == nil || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(b.Lease.Token)) != 1 || holder != "" && holder != b.Lease.Holder {
			return ErrLease
		}
		return nil
	})
}

// AcquireWorkspaceOwnerContext reserves ownership only. Run and setup perform
// their own preparation exactly once beneath this fresh acquisition.
func (s Store) AcquireWorkspaceOwnerContext(ctx context.Context, key, holder string) (*Bench, error) {
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	return s.workspaceLifecycle().Acquire(ctx, key, holder)
}

// FinishWorkspaceInitializationContext completes only the original prepared
// initialization owner; it never revives a stopped continuation.
func (s Store) FinishWorkspaceInitializationContext(ctx context.Context, key, token string) (*Bench, error) {
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	return s.workspaceLifecycle().FinishInitialization(ctx, key, token)
}

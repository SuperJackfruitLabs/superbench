package quotaoperationjournal

import (
	"context"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

// MaintainDenied holds operations.lock while a separately authorized maintenance
// action handles an existing, permanently denied identity. It never reserves or
// revives an operation. The action must enforce its own provenance, controller,
// approval and storage checks and be idempotent after a lost reply or failure.
func (s *Store) MaintainDenied(ctx context.Context, action func() error) error {
	if action == nil {
		return ErrAuthority
	}
	backend, err := s.backend(ctx)
	if err != nil {
		return err
	}
	return backend.withinExisting(ctx, func(j *Journal) error {
		return j.maintainDenied(s.identity, action)
	})
}

func (j *Journal) maintainDenied(identity quotaoperation.Identity, action func() error) error {
	check := func() error {
		found, denied, err := j.Lookup(identity)
		if err != nil {
			return err
		}
		if !found || !denied {
			return ErrAuthority
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	// A visible denied row may come from a writer that stopped before its
	// acknowledgement. Establish durability before any maintenance effect.
	if j.durable == nil {
		return ErrAuthority
	}
	if err := j.durable(); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	if err := action(); err != nil {
		return err
	}
	return check()
}

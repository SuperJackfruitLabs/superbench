package quotaoperationjournal

import (
	"context"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"os"
)

// Store is an operation-scoped immutable handle derived only from Control.
type Store struct {
	control  *Control
	identity quotaoperation.Identity
	verify   func(quotaoperation.Identity) error
	syncFile func(*os.File) error // private durability fault seam; nil uses File.Sync
}

func (s *Store) backend(ctx context.Context) (journalStore, error) {
	if s == nil || s.control == nil || s.verify == nil || !s.identity.Valid() {
		return journalStore{}, ErrAuthority
	}
	check := func() error {
		if err := s.control.check(ctx); err != nil {
			return err
		}
		if s.identity.Authority != s.control.authority {
			return ErrAuthority
		}
		return s.verify(s.identity)
	}
	if err := check(); err != nil {
		return journalStore{}, err
	}
	identity := s.identity
	return journalStore{Directory: s.control.config.Directory, owner: s.control.owner, Identity: &identity, Check: check, syncFile: s.syncFile}, nil
}

// Apply reserves before effects and retains the same operations.lock through
// fixed effects and final checks. Failure never implies completion or denial.
// An exact retry may call effect again: it must be idempotent for the retained
// immutable identity. A reservation is never an effect-completion receipt.
func (s *Store) Apply(ctx context.Context, effect func() error) error {
	if effect == nil {
		return ErrAuthority
	}
	backend, err := s.backend(ctx)
	if err != nil {
		return err
	}
	return backend.withinExisting(ctx, func(j *Journal) error {
		if err := j.Reserve(s.identity); err != nil {
			return err
		}
		if err := j.ready(); err != nil {
			return err
		}
		return effect()
	})
}

// Cancel proves only this original helper identity is durably denied. No
// execution fencing, controller closure or service conclusion is implied.
func (s *Store) Cancel(ctx context.Context) (quotaoperation.Proof, error) {
	backend, err := s.backend(ctx)
	if err != nil {
		return quotaoperation.Proof{}, err
	}
	if err = backend.withinExisting(ctx, func(j *Journal) error { return j.Deny(s.identity) }); err != nil {
		return quotaoperation.Proof{}, err
	}
	return quotaoperation.Proof{Version: quotaoperation.Version, Identity: s.identity, Cancelled: true}, nil
}

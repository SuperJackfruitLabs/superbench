package quotaoperationjournal

import (
	"context"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

// Apply opens only an existing journal. Its durable reservation, final authority
// checks and fixed helper effect share the ledger-wide operations lock. A failed
// effect retains the reservation; it never means completion or cancellation.
func (s Store) Apply(ctx context.Context, effect func() error) error {
	if effect == nil || !s.anchored || s.anchoredCheck == nil || s.Directory != s.anchoredDirectory {
		return ErrAuthority
	}
	s = s.forEffect()
	return s.withinExisting(ctx, func(j *Journal) error {
		if err := j.Reserve(s.Identity); err != nil {
			return err
		}
		if err := j.ready(); err != nil {
			return err
		}
		return effect()
	})
}

// Cancel returns the original identity's proof only after the deny transaction,
// readback and enclosing final authority checks succeed. It neither initializes
// missing history nor proves that a controller or execution has been stopped.
func (s Store) Cancel(ctx context.Context) (quotaoperation.Proof, error) {
	if !s.anchored || s.anchoredCheck == nil || s.Directory != s.anchoredDirectory {
		return quotaoperation.Proof{}, ErrAuthority
	}
	s = s.forEffect()
	err := s.withinExisting(ctx, func(j *Journal) error { return j.Deny(s.Identity) })
	if err != nil {
		return quotaoperation.Proof{}, err
	}
	return quotaoperation.Proof{Version: quotaoperation.Version, Identity: s.Identity, Cancelled: true}, nil
}

// Retain the constructor's pinned checks even if exported configuration is later
// changed. Additional caller checks may refuse, but cannot remove the binding.
func (s Store) forEffect() Store {
	pinned := s.anchoredCheck
	extra := s.Check
	s.Check = func(i quotaoperation.Identity) error {
		if err := pinned(i); err != nil {
			return err
		}
		if extra == nil {
			return ErrAuthority
		}
		return extra(i)
	}
	return s
}

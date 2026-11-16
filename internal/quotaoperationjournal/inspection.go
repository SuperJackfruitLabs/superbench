package quotaoperationjournal

import (
	"context"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

// Observation describes the exact original identity's retained journal state.
// Neither a reservation nor its absence proves effects completed or never ran.
// It grants no admission, cleanup, quota release or cancellation authority.
type Observation struct {
	Identity      quotaoperation.Identity
	Found, Denied bool
}

// Inspect holds the existing operations.lock through an exact identity lookup,
// the reader and final custody checks. Unlike Apply it never reserves an unknown
// identity and permits reading a denied one without reviving it. The reader must
// only inspect retained evidence; any later maintenance action needs its own
// independently verified authority and must recheck under the same lock.
// A retained Observation is a snapshot, not a capability or completion receipt.
func (s *Store) Inspect(ctx context.Context, reader func(Observation) error) error {
	if reader == nil {
		return ErrAuthority
	}
	backend, err := s.backend(ctx)
	if err != nil {
		return err
	}
	return backend.withinExisting(ctx, func(j *Journal) error {
		found, denied, err := j.Lookup(s.identity)
		if err != nil {
			return err
		}
		return reader(Observation{Identity: s.identity, Found: found, Denied: denied})
	})
}

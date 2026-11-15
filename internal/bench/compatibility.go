package bench

import (
	"errors"
	"sort"
)

var ErrWorkspace = errors.New("aggregate workspace requires the workspace API")

// RequireLegacy rejects an aggregate before scalar admission, Git supervision,
// log allocation or receipt reservation. Missing keys retain ordinary scalar
// create/not-found behavior. An unkeyed scalar projection must be complete.
func (s Store) RequireLegacy(key string) error {
	return s.WithLegacy(key, nil)
}

// WithLegacy runs fn after the kind check while retaining the shared state
// lock. The caller must initialize state first when serializing external
// effects with mutations; fn must not reenter a state mutation.
func (s Store) WithLegacy(key string, fn func() error) error {
	return s.readKinds(func(db database) error {
		if key != "" {
			if b := db[key]; b != nil && b.Workspace != nil {
				return ErrWorkspace
			}
		} else {
			for _, b := range db {
				if b.Workspace != nil {
					return ErrWorkspace
				}
			}
		}
		if fn != nil {
			return fn()
		}
		return nil
	})
}

// Status is the scalar API projection. It refuses the complete result whenever
// an aggregate would otherwise be omitted or misrepresented as a single repo.
func (s Store) Status(key string) ([]Bench, error) {
	return s.snapshot(key, true)
}

// Snapshot is the redacted internal maintenance view across record kinds.
func (s Store) Snapshot(key string) ([]Bench, error) {
	return s.snapshot(key, false)
}

func (s Store) snapshot(key string, legacy bool) ([]Bench, error) {
	var result []Bench
	err := s.read(func(db database) error {
		appendBench := func(b *Bench) error {
			if legacy && b.Workspace != nil {
				return ErrWorkspace
			}
			copy := *b
			copy.Lease = publicLease(b.Lease)
			result = append(result, copy)
			return nil
		}
		if key != "" {
			b := db[key]
			if b == nil {
				return ErrNotFound
			}
			return appendBench(b)
		}
		for _, b := range db {
			if err := appendBench(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, nil
}

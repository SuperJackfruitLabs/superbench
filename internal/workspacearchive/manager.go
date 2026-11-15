// Package workspacearchive owns aggregate archive and reviewed deletion authority.
// Filesystem snapshots and permission transitions remain in package reap.
package workspacearchive

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statepath"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

type Manager struct {
	State, Data  string
	Timeout      time.Duration
	Within       func(func(workspace.Update) error) error
	Git          func(context.Context, string, ...string) (string, error)
	SaveIntent   func(string, []byte) error
	ReleaseQuota func(*model.Bench) error
	AfterDelete  func() error
	Busy         error
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

func (m Manager) bounded(ctx context.Context, key string) (context.Context, context.CancelFunc, error) {
	if !keyPattern.MatchString(key) || m.Within == nil || m.Git == nil {
		return nil, nil, errors.New("aggregate archive requires valid identity, storage and managed Git")
	}
	if err := statepath.Validate(m.State, m.Data); err != nil {
		return nil, nil, err
	}
	if filepath.Clean(m.State) != m.State || filepath.Clean(m.Data) != m.Data {
		return nil, nil, errors.New("aggregate archive storage paths must be normalized")
	}
	timeout := m.Timeout
	if timeout == 0 {
		timeout = repository.DefaultTimeout
	}
	if timeout < 0 || timeout > time.Hour {
		return nil, nil, errors.New("invalid aggregate archive timeout")
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	if err := bounded.Err(); err != nil {
		cancel()
		return nil, nil, err
	}
	return bounded, cancel, nil
}
func (m Manager) lifecycle() workspace.Lifecycle {
	return workspace.Lifecycle{State: m.State, Data: m.Data, Timeout: m.Timeout, Git: m.Git}
}
func (m Manager) freezeJournal(key string) string {
	return filepath.Join(m.State, "aggregate-freeze-"+key+".json")
}
func (m Manager) reapJournal(key string) string {
	return filepath.Join(m.State, "aggregate-reap-"+key+".json")
}
func (m Manager) record(b *model.Bench) error {
	if err := workspacepolicy.Validate(b); err != nil {
		return err
	}
	if b.Path != filepath.Join(m.Data, "benches", b.Key) {
		return errors.New("aggregate archive root differs from configured data")
	}
	return nil
}
func (m Manager) available(b *model.Bench) error {
	if err := m.record(b); err != nil {
		return err
	}
	if b.Lease != nil {
		if m.Busy != nil {
			return m.Busy
		}
		return errors.New("aggregate workspace still has a writer")
	}
	if b.Workspace.Pending != nil || b.Workspace.Checkpoint == nil || b.Workspace.Stage != nil {
		return errors.New("aggregate archive requires a complete stopped checkpoint")
	}
	return nil
}
func (m Manager) checkout(ctx context.Context, b *model.Bench) error {
	if err := m.available(b); err != nil {
		return err
	}
	l := m.lifecycle()
	vector, err := l.Inspect(ctx, b, true)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(vector, b.Workspace.Checkpoint.Members) {
		return errors.New("aggregate checkout differs from its complete checkpoint")
	}
	if err := m.registrations(ctx, b, false, false); err != nil {
		return err
	}
	return l.Confirm(ctx, b)
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Archive commits the complete permission intent before changing any mode.
// Retries complete that exact intent without rerunning the completion trigger.
func (m Manager) Archive(ctx context.Context, key string, grace time.Duration, check func(context.Context, *model.Bench) error) (*model.Bench, error) {
	ctx, cancel, err := m.bounded(ctx, key)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if err := archivepolicy.Duration(grace); err != nil {
		return nil, err
	}
	if m.SaveIntent == nil {
		return nil, errors.New("aggregate archive requires durable intent storage")
	}
	var committed *model.Bench
	err = m.Within(func(update workspace.Update) error {
		var observed *model.Bench
		if err := update(func(rows benchstate.Database, _ *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			b := rows[key]
			if err := m.available(b); err != nil {
				return err
			}
			committed = workspace.Clone(b)
			switch b.Workspace.Phase {
			case "ready":
				if err := m.checkout(ctx, b); err != nil {
					return err
				}
				freeze, err := reap.NewFreeze(key, b.Path, b.Workspace.Checkpoint.Seal)
				if err != nil {
					return err
				}
				candidate := workspace.Clone(b)
				candidate.Workspace.Phase = "archived"
				candidate.Workspace.Archive = &model.WorkspaceArchive{ArchivedAt: now(), FreezeDigest: freeze.Digest}
				candidate.Workspace.Archive.ReapAfter, err = archivepolicy.Deadline(candidate.Workspace.Archive.ArchivedAt, grace)
				if err != nil {
					return err
				}
				authority := *candidate.Workspace.Archive
				if check != nil {
					if err := check(ctx, candidate); err != nil {
						return err
					}
				}
				if candidate.Workspace == nil || candidate.Workspace.Archive == nil || candidate.Workspace.Phase != "archived" {
					return errors.New("archive trigger removed its permission intent")
				}
				actualAuthority := *candidate.Workspace.Archive
				actualAuthority.Merge = nil
				if !reflect.DeepEqual(actualAuthority, authority) {
					return errors.New("archive trigger changed its permission intent")
				}
				candidate.Workspace.Archive.ArchivedAt = now()
				candidate.Workspace.Archive.ReapAfter, err = archivepolicy.Deadline(candidate.Workspace.Archive.ArchivedAt, grace)
				if err != nil {
					return err
				}
				// A trigger cannot grant authority to changed bytes or a different identity.
				expected := workspace.Clone(candidate)
				expected.Workspace.Archive = nil
				expected.Workspace.Phase = b.Workspace.Phase
				if !reflect.DeepEqual(expected, b) {
					return errors.New("archive trigger changed workspace identity or checkpoint")
				}
				if err := m.checkout(ctx, b); err != nil {
					return err
				}
				if err := reap.FreezePreflight(b.Path, freeze.Tree); err != nil {
					return err
				}
				raw, err := freeze.Bytes()
				if err != nil {
					return err
				}
				if err := m.SaveIntent(m.freezeJournal(key), raw); err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				rows[key] = candidate
			case "archived", "reaping", "reaped":
			default:
				return errors.New("aggregate initialization is not complete")
			}
			observed = workspace.Clone(rows[key])
			return nil
		}); err != nil {
			return err
		}
		committed = observed
		// Expose only successfully committed state.
		if committed.Workspace.Phase != "archived" {
			return nil
		}
		prior := workspace.Clone(committed)
		var finished *model.Bench
		err := update(func(rows benchstate.Database, _ *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			b := rows[key]
			if !reflect.DeepEqual(b, prior) {
				return errors.New("aggregate archive intent changed")
			}
			if err := m.checkout(ctx, b); err != nil {
				return err
			}
			freeze, err := reap.ReadFreeze(m.freezeJournal(key), key, b.Path, b.Workspace.Checkpoint.Seal, b.Workspace.Archive.FreezeDigest)
			if err != nil {
				return err
			}
			if err := reap.Freeze(b.Path, freeze.Tree); err != nil {
				return err
			}
			if err := m.checkout(ctx, b); err != nil {
				return err
			}
			if b.Workspace.Archive.FrozenAt == "" {
				b.Workspace.Archive.FrozenAt = now()
			}
			finished = workspace.Clone(b)
			return nil
		})
		if err == nil {
			committed = finished
		}
		return err
	})
	return workspace.Clone(committed), err
}

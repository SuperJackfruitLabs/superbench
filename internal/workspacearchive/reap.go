package workspacearchive

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

// Reap requires the concrete reviewed frozen tree. Its durable approval survives
// interrupted deletion, registration cleanup and quota retirement independently.
func (m Manager) Reap(ctx context.Context, approval Plan) (*model.Bench, error) {
	if err := reap.RequireNonRoot(); err != nil {
		return nil, err
	}
	ctx, cancel, err := m.bounded(ctx, approval.Key)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if m.SaveIntent == nil {
		return nil, errors.New("aggregate reap requires durable intent storage")
	}
	raw, err := approval.Bytes()
	if err != nil {
		return nil, err
	}
	approved, err := ReadPlan(raw)
	if err != nil {
		return nil, err
	}
	if approved.Version == MissingPlanVersion {
		return m.reapMissing(ctx, *approved, raw)
	}
	if approved.Missing != nil {
		return nil, errors.New("ordinary approval cannot carry missing-source evidence")
	}
	var committed *model.Bench
	err = m.Within(func(update workspace.Update) error {
		var observed *model.Bench
		if err := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			b := rows[approved.Key]
			if err := m.available(b); err != nil {
				return err
			}
			committed = workspace.Clone(b)
			if !matches(approved, b) {
				return errors.New("aggregate reap approval conflicts with immutable workspace state")
			}
			a := b.Workspace.Archive
			if a.FrozenAt == "" {
				return errors.New("aggregate archive read-only transition is incomplete")
			}
			if a.ReapingDigest != "" {
				if a.ReapingDigest != approved.Digest {
					return errors.New("another aggregate reap intent is retained")
				}
				saved, err := readPlanFile(m.reapJournal(b.Key))
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(saved, approved) {
					return errors.New("retained aggregate reap journal changed")
				}
				observed = workspace.Clone(b)
				return nil
			}
			if b.ProjectID != 0 && m.ReleaseQuota == nil {
				return errors.New("aggregate reap requires quota retirement authority")
			}
			if err := archivepolicy.Eligible(a.ArchivedAt, a.ReapAfter, time.Now()); err != nil {
				return err
			}
			current, err := m.plan(ctx, tx, b)
			if err != nil {
				return err
			}
			if current.Digest != approved.Digest {
				return errors.New("aggregate changed after review; approve a fresh reap plan")
			}
			if err := m.SaveIntent(m.reapJournal(b.Key), raw); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			b.Workspace.Phase = "reaping"
			a.ReapingDigest = approved.Digest
			observed = workspace.Clone(b)
			return nil
		}); err != nil {
			return err
		}
		committed = observed
		if committed.Workspace.Phase == "reaped" {
			return nil
		}
		var finished *model.Bench
		err := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			b := rows[approved.Key]
			if !reflect.DeepEqual(b, committed) {
				return errors.New("aggregate reap intent changed")
			}
			// Confirmation does not require surviving checkout paths; every retry
			// proves the retained seal, all pins, task refs and mirror identities.
			if err := m.transferJournal(tx, b); err != nil {
				return err
			}
			if err := m.confirm(ctx, b); err != nil {
				return err
			}
			if err := m.registrations(ctx, b, true, false, approved.Registrations); err != nil {
				return err
			}
			if _, err := os.Lstat(b.Path); err == nil {
				if err := reap.DeleteFrozenContents(b.Path, approved.Tree); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			if m.AfterDelete != nil {
				if err := m.AfterDelete(); err != nil {
					return err
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := emptyApprovedRoot(b.Path, approved.Tree); err != nil {
				return err
			}
			if err := m.registrations(ctx, b, true, true, approved.Registrations); err != nil {
				return err
			}
			if b.ProjectID != 0 {
				if m.ReleaseQuota == nil {
					return errors.New("aggregate quota retirement authority is unavailable")
				}
				if err := m.ReleaseQuota(workspace.Clone(b)); err != nil {
					return err
				}
			} else if err := os.Remove(b.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			b.Workspace.Phase = "reaped"
			b.Workspace.Archive.ReapedAt = now()
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

// Deletion may add only owner-write to the approved root. A replacement root or
// any newly created entry cannot be passed to registration/quota cleanup.
func emptyApprovedRoot(path string, tree reap.Tree) error {
	actual, err := reap.Snapshot(path, nil)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	writable := tree.Root
	writable.Mode |= 0200
	if len(actual.Entries) != 0 || actual.Root != tree.Root && actual.Root != writable {
		return errors.New("aggregate root changed after approved deletion")
	}
	return nil
}

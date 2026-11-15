package workspace

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"regexp"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

var writerHolder = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,199}$`)

// Acquire reserves one workspace-wide preparation owner. The higher-level
// lease/run operation must prepare its members before handing off runnable
// ownership. Existing dirty work is preserved, as with scalar acquisition.
// Remote refresh completes under the host lock before publishing the lease;
// refresh failure cannot leave a new token or silently replace another holder.
func (l Lifecycle) Acquire(ctx context.Context, key, holder string) (*model.Bench, error) {
	ctx, cancel, err := l.bounded(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if !keyPattern.MatchString(key) || !writerHolder.MatchString(holder) || l.Within == nil || l.Stopped == nil || l.Admit == nil || l.Allocate == nil {
		return nil, errors.New("workspace acquisition requires a valid holder, storage, admission and allocation authority")
	}
	var committed *model.Bench
	err = l.Within(func(update Update) error {
		if err := update(func(rows benchstate.Database, _ *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			b := rows[key]
			if err := workspacepolicy.Validate(b); err != nil {
				return err
			}
			if b.Workspace.Phase != "ready" || b.Workspace.Pending != nil || b.Lease != nil {
				// The requester has not acquired this owner, so no private record
				// containing another holder's capability may escape on refusal.
				return errors.New("workspace is not available for acquisition; no automatic lease expiry")
			}
			if err := l.Stopped(privateRecord(b)); err != nil {
				return err
			}
			committed = privateRecord(b)
			return nil
		}); err != nil {
			return err
		}
		before, err := l.Inspect(ctx, committed, false)
		if err != nil {
			return err
		}
		if err := l.Admit(); err != nil {
			return err
		}
		allocated := privateRecord(committed)
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := l.Allocate(allocated); err != nil {
			return err
		}
		if committed.ProjectID != 0 && (allocated.ProjectID != committed.ProjectID || allocated.QuotaBytes != committed.QuotaBytes || allocated.QuotaInodes != committed.QuotaInodes) {
			return errors.New("workspace allocator changed an assigned quota")
		}
		identity := privateRecord(allocated)
		identity.ProjectID, identity.QuotaBytes, identity.QuotaInodes = committed.ProjectID, committed.QuotaBytes, committed.QuotaInodes
		if !reflect.DeepEqual(identity, committed) {
			return errors.New("workspace allocator changed non-allocation metadata")
		}
		if err := workspacepolicy.Validate(allocated); err != nil {
			return err
		}
		mirrors := repository.Mirrors{State: l.State, Data: l.Data, Timeout: l.Timeout}
		seen := map[string]bool{}
		for _, member := range allocated.Workspace.Members {
			if seen[member.Repository] {
				continue
			}
			seen[member.Repository] = true
			// Refresh refuses changed/missing mirrors; Prepare would reclone and
			// could silently adopt a different cache beneath registered work.
			if err := mirrors.Refresh(ctx, member.Repository, member.Mirror); err != nil {
				return err
			}
		}
		after, err := l.Inspect(ctx, allocated, false)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(before, after) {
			return errors.New("workspace HEAD vector changed during acquisition refresh")
		}
		candidate := privateRecord(allocated)
		candidate.Lease, err = model.NewLease(holder)
		if err != nil {
			return err
		}
		candidate.Lease.Continuation, err = model.Nonce()
		if err != nil {
			return err
		}
		candidate.Workspace.Preparation = nil
		candidate.Workspace.Stage = nil
		if err := update(func(rows benchstate.Database, _ *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !reflect.DeepEqual(rows[key], committed) {
				return errors.New("workspace identity or availability changed during acquisition")
			}
			rows[key] = privateRecord(candidate)
			return nil
		}); err != nil {
			return err
		}
		committed = privateRecord(candidate)
		return nil
	})
	return privateRecord(committed), err
}

// Release revalidates stopped ownership and the exact complete clean checkpoint
// vector. It never creates a checkpoint or finalizes initial preparation. The
// caller must explicitly complete any pending generation before releasing.
func (l Lifecycle) Release(ctx context.Context, key, token string) (*model.Bench, error) {
	ctx, cancel, err := l.bounded(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if !keyPattern.MatchString(key) || token == "" || l.Within == nil || l.Stopped == nil || l.Released == nil {
		return nil, errors.New("workspace release requires storage, stopped ownership and retained-history authority")
	}
	var committed *model.Bench
	err = l.Within(func(update Update) error {
		if err := update(func(rows benchstate.Database, _ *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			b := rows[key]
			if err := l.owner(b, token); err != nil {
				return err
			}
			if b.Workspace.Phase != "ready" || b.Workspace.Pending != nil || b.Workspace.Checkpoint == nil {
				return errors.New("workspace release requires a complete operational checkpoint")
			}
			committed = privateRecord(b)
			return nil
		}); err != nil {
			return err
		}
		vector, err := l.Inspect(ctx, committed, true)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(vector, committed.Workspace.Checkpoint.Members) {
			return errors.New("workspace HEAD vector differs from its complete checkpoint")
		}
		if err := l.confirm(ctx, committed, committed.Workspace.Checkpoint); err != nil {
			return err
		}
		finished := privateRecord(committed)
		finished.Lease = nil
		finished.Workspace.Stage = nil
		if err := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			b := rows[key]
			if !reflect.DeepEqual(b, committed) {
				return errors.New("workspace release ownership or checkpoint changed")
			}
			if err := l.owner(b, token); err != nil {
				return err
			}
			current, err := l.Inspect(ctx, b, true)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(current, b.Workspace.Checkpoint.Members) {
				return errors.New("workspace HEAD vector changed before release")
			}
			if err := l.Released(tx, privateRecord(b)); err != nil {
				return err
			}
			rows[key] = privateRecord(finished)
			return ctx.Err()
		}); err != nil {
			return err
		}
		committed = privateRecord(finished)
		return nil
	})
	return privateRecord(committed), err
}

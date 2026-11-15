package workspacetransfer

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

func (m Manager) relinquishMissing(ctx context.Context, a workspacerepair.Manager, p MissingPlan) error {
	return a.Prep.Gate(p.SourceKey, func() error {
		return a.Lifecycle.Within(func(update workspace.Update) error {
			var done bool
			guard := func() error {
				return update(func(rows benchstate.Database, tx *sql.Tx) error {
					j, b, err := boundRecord(rows, tx, p.ID)
					if err != nil {
						return err
					}
					if j.Digest != p.Digest || j.TargetID != p.TargetID || j.SourceID != p.SourceID || !reflect.DeepEqual(b.Workspace.Transfer.Recovery, &p.Recovery) {
						return errors.New("missing-source approval no longer owns its retirement")
					}
					done = j.Phase != "started"
					return ctx.Err()
				})
			}
			if err := guard(); err != nil || done {
				return err
			}
			present := worktreetransfer.Manager{Git: a.Prep.Git, Timeout: m.Store.MirrorTimeout, Gate: func(context.Context, worktreetransfer.Plan) error { return guard() }}
			missing := m.missingGit(a)
			missing.Gate = func(context.Context, worktreetransfer.MissingPlan) error { return guard() }
			// Validate the entire present/missing vector before the first effect.
			// A later stale member must not detach an earlier valid registration.
			for _, member := range p.Members {
				var err error
				if member.Present != nil {
					err = present.Preflight(ctx, *member.Present)
				} else {
					err = missing.Preflight(ctx, *member.Missing)
				}
				if err != nil {
					return err
				}
			}
			for _, member := range p.Members {
				var err error
				if member.Present != nil {
					err = present.Apply(ctx, *member.Present)
				} else {
					err = missing.Apply(ctx, *member.Missing)
				}
				if err != nil {
					return err
				}
			}
			if err := m.verifyMissingRetained(ctx, a, p); err != nil {
				return err
			}
			return update(func(rows benchstate.Database, tx *sql.Tx) error {
				j, b, err := boundRecord(rows, tx, p.ID)
				if err != nil {
					return err
				}
				if j.Phase != "started" || !reflect.DeepEqual(b.Workspace.Transfer.Recovery, &p.Recovery) {
					return errors.New("missing-source retirement changed during relinquishment")
				}
				if err = a.Lifecycle.Confirm(ctx, b); err != nil {
					return err
				}
				if err = reftransfer.Relinquish(tx, p.ID, p.Digest); err != nil {
					return err
				}
				b.Workspace.Transfer.Phase = "transferred"
				return ctx.Err()
			})
		})
	})
}

func (m Manager) verifyMissingRetained(ctx context.Context, a workspacerepair.Manager, p MissingPlan) error {
	if err := m.storageWorker().VerifyStorage(ctx, p.Storage); err != nil {
		return err
	}
	present := worktreetransfer.Manager{Git: a.Prep.Git, Timeout: m.Store.MirrorTimeout}
	missing := m.missingGit(a)
	for _, member := range p.Members {
		if member.Missing != nil {
			if err := missing.VerifyRetained(ctx, *member.Missing); err != nil {
				return err
			}
		} else {
			old := member.Present
			current, err := present.InspectDetached(ctx, old.Checkout, old.Common, old.TaskRef, old.Commit)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(current, *old) {
				return errors.New("retained source files or administration changed")
			}
		}
	}
	return nil
}

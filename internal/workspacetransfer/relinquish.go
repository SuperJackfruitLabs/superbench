package workspacetransfer

import (
	"context"
	"database/sql"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

func relinquish(ctx context.Context, a workspacerepair.Manager, p Plan) error {
	return a.Prep.Gate(p.SourceKey, func() error {
		return a.Lifecycle.Within(func(update workspace.Update) error {
			var done bool
			guard := func() error {
				return update(func(rows benchstate.Database, tx *sql.Tx) error {
					j, _, err := boundRecord(rows, tx, p.ID)
					if err != nil {
						return err
					}
					if j.Digest != p.Digest || j.TargetID != p.TargetID || j.SourceID != p.SourceID {
						return errors.New("transfer approval no longer owns this source")
					}
					done = j.Phase != "started"
					return ctx.Err()
				})
			}
			if err := guard(); err != nil || done {
				return err
			}
			w := worktreetransfer.Manager{Git: a.Prep.Git, Timeout: a.Lifecycle.Timeout, Gate: func(context.Context, worktreetransfer.Plan) error { return guard() }}
			for _, member := range p.Members {
				if err := w.Apply(ctx, member.Worktree); err != nil {
					return err
				}
			}
			// Recheck the complete detached inventory before publishing handoff.
			for _, member := range p.Members {
				if err := w.Apply(ctx, member.Worktree); err != nil {
					return err
				}
			}
			return update(func(rows benchstate.Database, tx *sql.Tx) error {
				j, b, err := boundRecord(rows, tx, p.ID)
				if err != nil {
					return err
				}
				if j.Phase != "started" {
					return errors.New("transfer phase changed during branch relinquishment")
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

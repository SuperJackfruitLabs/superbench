package remotestate

import (
	"context"
	"database/sql"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteschema"
)

// Fence kills the lease's remote scope, waits for it to empty, then in one
// transaction clears the execution and ends its open sessions as "fenced".
// It clears a remote execution in any phase: workspaceprep's stop leaves one
// stopped, and release would otherwise wait on history it never has. Without
// a remote execution it touches nothing. A failed kill changes no state.
// admin skips the token check, as stop_admin does.
func (m Manager) Fence(ctx context.Context, key, token string, admin bool) error {
	if err := m.valid(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.Prep.Gate(key, func() error {
		var prior *execution.Execution
		if err := m.Prep.Read(func(rows benchstate.Database) error {
			b := rows[key]
			if b == nil || b.Lease == nil || b.Lease.Execution == nil || b.Lease.Execution.Kind != execution.KindRemote {
				return nil
			}
			if !admin && !owns(b.Lease, token) {
				return bench.ErrLease
			}
			prior = clone(b.Lease.Execution)
			return nil
		}); err != nil || prior == nil {
			return err
		}
		if err := m.Scopes.Fence(prior); err != nil {
			return errors.Join(ErrUnavailable, err)
		}
		if err := m.Scopes.Empty(prior); err != nil {
			return errors.Join(ErrUnavailable, err)
		}
		return m.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b := rows[key]
			if b == nil || b.Workspace == nil || b.Lease == nil {
				return ErrConflict
			}
			if !admin && !owns(b.Lease, token) {
				return bench.ErrLease
			}
			if e := b.Lease.Execution; e == nil || e.Kind != execution.KindRemote || e.ID != prior.ID {
				return ErrConflict
			}
			b.Lease.Execution = nil
			return remoteschema.EndOpenSessions(tx, b.Workspace.ID, m.now(), "fenced")
		})
	})
}

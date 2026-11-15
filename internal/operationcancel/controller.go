package operationcancel

import (
	"context"
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

// Reconcile operates only after the service's durable cancellation barrier.
// It returns no capabilities and never acknowledges mere context cancellation.
// Admission must independently certify helper tracking before supplying this
// closed inventory, including the meaningful empty-inventory case.
func (m Manager) Reconcile(ctx context.Context, i operationcontrol.Intent, helpers []operationcontrol.Helper) error {
	if len(helpers) > operationcontrol.MaxHelpers {
		return operationcontrol.ErrAuthority
	}
	seen := map[string]bool{}
	for _, h := range helpers {
		key := h.Kind + ":" + h.Key
		if !h.Valid(i) || h.State != m.State || seen[key] {
			return operationcontrol.ErrAuthority
		}
		seen[key] = true
	}
	inventory, err := m.Inspect(i)
	if err != nil {
		return err
	}
	m.Expected = &inventory
	// No host lock, SQL transaction, receipt mutex or ordinary operation slot is
	// held over these privileged waits. The deny barrier waits out atomic apply
	// and permanently excludes queued/retried invocations of the same epoch.
	for _, h := range helpers {
		if _, err = quotahelper.Cancel(ctx, i, h); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = m.recoverGit(i); err != nil {
		return err
	}
	if err = m.Revoke(i); err != nil {
		return err
	}
	var current bool
	err = m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		b := rows[i.Key]
		v, e := m.inventory(tx, i, b)
		if e != nil {
			return e
		}
		if v.Binding.Phase == "bound" && b != nil && b.Lease != nil && b.Lease.Execution != nil {
			for _, run := range v.Runs {
				if run.Execution.ID == b.Lease.Execution.ID {
					current = true
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if current {
		if err = m.ReconcileInterruption(i); err != nil {
			return err
		}
	}
	return m.Conclude(i)
}
func (m Manager) recoverGit(i operationcontrol.Intent) error {
	if m.Gate == nil || m.Update == nil {
		return operationcontrol.ErrAuthority
	}
	return m.Gate(i.Key, func() error {
		return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			v, err := m.inventory(tx, i, rows[i.Key])
			if err != nil {
				return err
			}
			recover := m.RecoverGit
			if recover == nil {
				recover = func(c operationcontrol.Control, i operationcontrol.Intent) error {
					return gitcontrol.RecoverOperation(m.State, c, i)
				}
			}
			return recover(v.Binding.Control, i)
		})
	})
}

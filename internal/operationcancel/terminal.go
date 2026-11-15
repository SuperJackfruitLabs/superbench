package operationcancel

import (
	"database/sql"
	"errors"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
)

type Inventory struct {
	Binding operationcontrol.Binding
	Runs    []operationcontrol.Run
}

func (m Manager) inventory(tx *sql.Tx, i operationcontrol.Intent, b *model.Bench) (Inventory, error) {
	var out Inventory
	found, err := operationcontrol.Get(tx, i.Target)
	if err != nil {
		return out, err
	}
	if found.Intent != i || !found.Control.Managed() || found.Control.State != m.State {
		return out, operationcontrol.ErrAuthority
	}
	out.Binding = found
	out.Runs, err = operationcontrol.Runs(tx, i)
	if err != nil {
		return out, err
	}
	if m.Expected != nil {
		old, now := m.Expected.Binding, out.Binding
		old.Phase = now.Phase
		if old != now || len(m.Expected.Runs) != len(out.Runs) {
			return out, operationcontrol.ErrAuthority
		}
		for n := range out.Runs {
			if m.Expected.Runs[n] != out.Runs[n] {
				return out, operationcontrol.ErrAuthority
			}
		}
	}
	if found.Phase == "admitted" {
		if len(out.Runs) != 0 {
			return out, operationcontrol.ErrAuthority
		}
		return out, nil
	}
	if found.Phase == "finished" || found.Phase == "stopped" {
		return out, nil
	}
	if found.Phase != "bound" {
		return out, operationcontrol.ErrAuthority
	}
	if b != nil && b.Workspace != nil && b.Workspace.ID == found.BenchID && b.Lease == nil {
		if b.Workspace.Phase != "ready" || b.Workspace.Pending != nil || b.Workspace.Stage != nil {
			return out, operationcontrol.ErrAuthority
		}
		return out, nil
	}
	if err = operationcontrol.RequireOwner(tx, i, b); err != nil {
		return out, err
	}
	if len(b.Lease.RetainedRuns) > model.WorkspaceRetainedRuns {
		return out, operationcontrol.ErrAuthority
	}
	// Unlabelled earlier runs cannot be adopted as this request's interrupted
	// history. They must already have an actual sealed outcome before effects.
	for _, id := range b.Lease.RetainedRuns {
		if err = sealed(tx, id, i.Key, found.Holder); err != nil {
			return out, err
		}
	}
	if e := b.Lease.Execution; e != nil {
		own := false
		for _, run := range out.Runs {
			if run.Execution.ID == e.ID {
				expected, actual := run.Execution, *e
				actual.PID = expected.PID
				actual.Phase = expected.Phase
				if actual != expected {
					return out, operationcontrol.ErrAuthority
				}
				own = true
				break
			}
		}
		if !own {
			if e.Phase != "stopped" {
				return out, operationcontrol.ErrAuthority
			}
			if err = sealed(tx, e.ID, i.Key, found.Holder); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}
func sealed(tx *sql.Tx, id, key, holder string) error {
	r, err := history.Get(tx, id)
	if err != nil {
		return err
	}
	if r.Key != key || r.Holder != holder || r.Origin != "supervised" || r.SealedAt.IsZero() || r.File == nil && r.Interruption == nil {
		return errors.New("operation history requires reconciliation")
	}
	return nil
}

// Inspect validates the complete bounded authority inventory without fencing.
// A controller must do this before waiting on original helper deny barriers,
// then recheck the same binding and run vector before further effects.
func (m Manager) Inspect(i operationcontrol.Intent) (Inventory, error) {
	var out Inventory
	if !i.Valid() || m.Update == nil {
		return out, operationcontrol.ErrAuthority
	}
	err := m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		var err error
		out, err = m.inventory(tx, i, rows[i.Key])
		return err
	})
	return out, err
}

// Revoke fences only the exact bound owner under its launch gate. It neither
// adopts an admitted-but-unbound lease nor follows a key to a replacement owner.
func (m Manager) Revoke(i operationcontrol.Intent) error {
	if !i.Valid() || m.Update == nil || m.Gate == nil || m.Backend == nil {
		return operationcontrol.ErrAuthority
	}
	return m.Gate(i.Key, func() error {
		return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b := rows[i.Key]
			v, err := m.inventory(tx, i, b)
			if err != nil {
				return err
			}
			if v.Binding.Phase != "bound" || b.Lease == nil {
				return nil
			}
			if e := b.Lease.Execution; e != nil {
				own := false
				for _, run := range v.Runs {
					if run.Execution.ID == e.ID {
						own = true
						break
					}
				}
				if own {
					if err = m.Backend.Fence(e); err != nil {
						return err
					}
				}
				if err = m.Backend.Empty(e); err != nil {
					return err
				}
				e.Phase = "stopped"
			}
			b.Lease.StopRequested = true
			b.Lease.Continuation = ""
			return nil
		})
	})
}

// Conclude requires actual sealed history and independent scope emptiness; its
// caller must already have reconciled every helper and Git-control invocation.
func (m Manager) Conclude(i operationcontrol.Intent) error {
	if !i.Valid() || m.Update == nil || m.Gate == nil || m.Backend == nil {
		return operationcontrol.ErrAuthority
	}
	return m.Gate(i.Key, func() error {
		var locks []*os.File
		defer func() {
			for _, f := range locks {
				_ = f.Close()
			}
		}()
		return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b := rows[i.Key]
			v, err := m.inventory(tx, i, b)
			if err != nil {
				return err
			}
			for _, run := range v.Runs {
				if err = sealed(tx, run.Execution.ID, i.Key, v.Binding.Holder); err != nil {
					return err
				}
				if err = m.Backend.Empty(&run.Execution); err != nil {
					return err
				}
				r, err := history.Get(tx, run.Execution.ID)
				if err != nil {
					return err
				}
				f, entry, size, err := retention.ObserveInterrupted(m.State, run.Execution.ID, run.Execution.LogLimitBytes)
				if err != nil {
					return err
				}
				if f != nil {
					locks = append(locks, f)
				}
				if (entry == nil) != (r.File == nil) || entry != nil && *entry != *r.File || size != r.Size {
					return errors.New("sealed operation capture changed")
				}
				if err = retention.RecheckInterrupted(m.State, run.Execution.ID, run.Execution.LogLimitBytes, f, entry, size); err != nil {
					return err
				}
			}
			return operationcontrol.ConcludeCancellation(tx, i, b)
		})
	})
}

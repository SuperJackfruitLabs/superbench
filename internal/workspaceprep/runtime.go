// Package workspaceprep prepares registered members under one initialization lease.
package workspaceprep

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statepath"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/transferguard"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

type Backend interface {
	Create(string, string) (execution.Identity, error)
	Start(*exec.Cmd, *execution.Execution) error
	Fence(*execution.Execution) error
	Empty(*execution.Execution) error
}

type Manager struct {
	State, Data, CgroupRoot string
	RepairID, TransferID    string
	Update                  workspace.Update
	Read                    func(func(benchstate.Database) error) error
	Gate                    func(string, func() error) error
	Git                     func(context.Context, string, ...string) (string, error)
	Backend                 Backend
	Admit                   func() error
	Quota                   func(*model.Bench) error
	// FreshQuota verifies a Fresh workspace's pinned allocation read-only.
	FreshQuota func(*model.Bench) error
	ReserveLog func(benchstate.Database) (uint64, error)
	Seal       func(string, *output.Capture, *int, bool) error
	// Fresh selects the payload route for a Fresh tree; nil uses freshtree.Select.
	Fresh freshtree.Selector
}

func (m Manager) validate() error {
	if m.Update == nil || m.Read == nil || m.Gate == nil || m.Git == nil || m.Backend == nil || m.Admit == nil || m.Quota == nil || m.ReserveLog == nil || m.Seal == nil {
		return errors.New("workspace preparation requires state, fencing and storage authority")
	}
	if !filepath.IsAbs(m.State) || filepath.Clean(m.State) != m.State || !filepath.IsAbs(m.Data) || filepath.Clean(m.Data) != m.Data {
		return errors.New("workspace preparation storage paths must be absolute and normalized")
	}
	return statepath.Validate(m.State, m.Data)
}

// quota checks b's allocation before a launch. A Fresh allocation belongs to its
// retained bootstrap authority, and the helper refuses a legacy apply for it, so
// it is only verified read-only; a missing verifier fails closed.
func (m Manager) quota(b *model.Bench) error {
	fresh, err := freshtree.Selected(m.Fresh, m.State, b)
	if err != nil {
		return err
	}
	if !fresh {
		return m.Quota(b)
	}
	if m.FreshQuota == nil {
		return errors.New("fresh workspace launch requires read-only quota verification")
	}
	return m.FreshQuota(b)
}

func owned(rows benchstate.Database, key, token string) (*model.Bench, error) {
	b := rows[key]
	if b == nil || b.Workspace == nil {
		return nil, errors.New("registered aggregate workspace required")
	}
	if b.Lease == nil || token == "" || subtle.ConstantTimeCompare([]byte(b.Lease.Token), []byte(token)) != 1 {
		return nil, errors.New("workspace writer ownership required")
	}
	return b, nil
}

func (m Manager) preparable(b *model.Bench) error {
	if b.Workspace.Transfer != nil {
		return workspace.ErrTransfer
	}
	if err := transferguard.Authority(b, m.TransferID, m.RepairID); err != nil {
		return err
	}
	if b.Lease.StopRequested || b.Lease.Continuation == "" || b.Workspace.Pending != nil {
		return errors.New("workspace preparation is stopped or pending checkpoint")
	}
	if b.Workspace.Phase == "ready" {
		return nil
	}
	if b.Lease.Holder != "initialization" || (b.Workspace.Phase != "initializing" && b.Workspace.Phase != "prepared") {
		return errors.New("workspace preparation is outside a held operational or initialization lease")
	}
	return nil
}

func (m Manager) stopped(b *model.Bench) error {
	if b.Lease.Execution == nil {
		return nil
	}
	if b.Lease.Execution.Phase != "stopped" {
		return errors.New("workspace execution must be fenced first")
	}
	return m.Backend.Empty(b.Lease.Execution)
}

func (m Manager) inspectMember(ctx context.Context, b *model.Bench, name string) error {
	_, err := m.lifecycle().InspectMember(ctx, b, name, false)
	return err
}

func retain(tx *sql.Tx, b *model.Bench) error {
	l := b.Lease
	if l.Execution == nil {
		return nil
	}
	r, err := history.Get(tx, l.Execution.ID)
	if err != nil {
		return err
	}
	if len(l.RetainedRuns) >= model.WorkspaceRetainedRuns || r.Key != b.Key || r.File == nil || r.ExitCode == nil || !r.ReleasedAt.IsZero() {
		return errors.New("workspace stage requires retained sealed history before replacement")
	}
	l.RetainedRuns = append(l.RetainedRuns, r.ID)
	l.Execution = nil
	b.Workspace.Stage = nil
	return nil
}

// Stop fences a known workspace execution and revokes automatic continuation.
func (m Manager) Stop(key, token string, admin bool) (*model.Bench, error) {
	return m.stop(key, token, "", admin, false)
}

// completeExecution acknowledges only this runner's recorded scope. Stopping
// revokes launch authority, but must not prevent its live capturer from sealing
// the actual outcome after an administrative stop.
func (m Manager) completeExecution(key, token string, expected *execution.Execution) (*execution.Execution, error) {
	var result *execution.Execution
	err := m.Gate(key, func() error {
		if err := m.Read(func(rows benchstate.Database) error {
			b, err := owned(rows, key, token)
			if err != nil {
				return err
			}
			if !sameExecution(b.Lease.Execution, expected) {
				return errors.New("workspace runner execution was replaced")
			}
			return nil
		}); err != nil {
			return err
		}
		if err := m.Backend.Fence(expected); err != nil {
			return err
		}
		if err := m.Backend.Empty(expected); err != nil {
			return err
		}
		return m.Update(func(rows benchstate.Database, _ *sql.Tx) error {
			b, err := owned(rows, key, token)
			if err != nil {
				return err
			}
			if !sameExecution(b.Lease.Execution, expected) {
				return errors.New("workspace runner execution changed during completion")
			}
			b.Lease.Execution.Phase = "stopped"
			copy := *b.Lease.Execution
			result = &copy
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return result, err
}

func sameExecution(a, b *execution.Execution) bool {
	return a != nil && b != nil && a.ID == b.ID && a.Root == b.Root && a.HostID == b.HostID && a.BootID == b.BootID && a.Log == b.Log && a.LogLimitBytes == b.LogLimitBytes
}

func (m Manager) stop(key, token, continuation string, admin, internal bool) (*model.Bench, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	var result *model.Bench
	err := m.Gate(key, func() error {
		var prior *model.Bench
		if err := m.Read(func(rows benchstate.Database) error {
			b := rows[key]
			if b == nil || b.Workspace == nil || b.Lease == nil {
				return errors.New("leased aggregate workspace required")
			}
			if !admin {
				if _, err := owned(rows, key, token); err != nil {
					return err
				}
			}
			if continuation != "" && b.Lease.Continuation != continuation {
				return errors.New("workspace continuation revoked")
			}
			prior = workspace.Clone(b)
			return nil
		}); err != nil {
			return err
		}
		if prior.Lease.Execution != nil {
			if err := m.Backend.Fence(prior.Lease.Execution); err != nil {
				return err
			}
		}
		var candidate *model.Bench
		err := m.Update(func(rows benchstate.Database, _ *sql.Tx) error {
			b, err := owned(rows, key, prior.Lease.Token)
			if err != nil {
				return err
			}
			if continuation != "" && b.Lease.Continuation != continuation {
				return errors.New("workspace continuation revoked")
			}
			old, current := prior.Lease.Execution, b.Lease.Execution
			if (old == nil) != (current == nil) || old != nil && old.ID != current.ID {
				return errors.New("workspace execution changed during stop")
			}
			if current != nil {
				current.Phase = "stopped"
			}
			if !internal {
				b.Lease.Continuation = ""
				b.Lease.StopRequested = true
			}
			candidate = workspace.Clone(b)
			return nil
		})
		if err == nil {
			result = candidate
		} else {
			result = prior
		}
		return err
	})
	return result, err
}

// Reconcile cancellation at the public API boundary, including cache-only,
// skipped-proof and final-save paths which never enter the payload watcher.
func (m Manager) reconcileCancellation(ctx context.Context, key, token string, result **model.Bench, operationErr *error) {
	ctxErr := ctx.Err()
	if ctxErr == nil && !errors.Is(*operationErr, transferguard.ErrGuard) {
		return
	}
	_, stopErr := m.Stop(key, token, false)
	current, readErr := m.ownedCurrent(key, token)
	*result = current
	*operationErr = errors.Join(*operationErr, ctxErr, stopErr, readErr)
}

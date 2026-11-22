// Package workspacerenew gives root a successor owner for a ready workspace
// whose owner was stopped administratively.
//
// A stop revokes its lease's continuation for good, so a stopped owner can never
// launch again. When that owner still holds files the checkpoint refuses, root
// retires its token and issues a new one, with a fresh continuation, to the same
// holder. The old token stays stopped; the successor runs, checkpoints and
// releases through the ordinary verbs.
package workspacerenew

import (
	"database/sql"
	"errors"
	"reflect"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

// Every refusal is one of these static errors; none carries a path or token.
var (
	ErrRoot       = errors.New("workspace renewal requires local root")
	ErrUnknown    = errors.New("unknown workspace")
	ErrNotStopped = errors.New("workspace renewal requires an owner stopped with stop_admin")
	ErrLive       = errors.New("workspace renewal requires a stopped, empty execution")
	ErrState      = errors.New("workspace renewal requires a ready workspace with no archive, transfer, repair or pending checkpoint")
	ErrBusy       = errors.New("workspace renewal refused: an operation is still active on this workspace")
	ErrChanged    = errors.New("workspace owner changed during renewal")
)

type Manager struct{ Prep workspaceprep.Manager }

func renewable(b *model.Bench) error {
	if b == nil || b.Workspace == nil {
		return ErrUnknown
	}
	w := b.Workspace
	if w.Phase != "ready" || b.ArchivedAt != "" || w.Archive != nil || w.Transfer != nil || w.InitializationRepair != nil || w.Pending != nil || b.Lease != nil && b.Lease.Holder == "initialization" {
		return ErrState
	}
	if b.Lease == nil || !b.Lease.StopRequested || b.Lease.Continuation != "" {
		return ErrNotStopped
	}
	if b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped" {
		return ErrLive
	}
	return nil
}

// Renew replaces key's stopped owner with a successor and returns the saved
// bench, whose lease carries the new token. uid is the caller's real identity
// at the local administrative crossing; only root may renew.
func (m Manager) Renew(key string, uid uint32) (*model.Bench, error) {
	if uid != 0 {
		return nil, ErrRoot
	}
	p := m.Prep
	if p.Gate == nil || p.Read == nil || p.Update == nil || p.Backend == nil {
		return nil, errors.New("workspace renewal requires state and fencing authority")
	}
	var result *model.Bench
	err := p.Gate(key, func() error {
		var prior *model.Bench
		if err := p.Read(func(rows benchstate.Database) error {
			if err := renewable(rows[key]); err != nil {
				return err
			}
			prior = workspace.Clone(rows[key])
			return nil
		}); err != nil {
			return err
		}
		// Prove the stopped scope empty again before its owner is replaced.
		if e := prior.Lease.Execution; e != nil && (p.Backend.Fence(e) != nil || p.Backend.Empty(e) != nil) {
			return ErrLive
		}
		return p.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b := rows[key]
			if err := renewable(b); err != nil {
				return err
			}
			if b.Lease.Token != prior.Lease.Token || !reflect.DeepEqual(b.Lease.Execution, prior.Lease.Execution) {
				return ErrChanged
			}
			if err := operationcontrol.RequireContext(tx, b, nil); errors.Is(err, operationcontrol.ErrBusy) {
				return ErrBusy
			} else if err != nil {
				return err
			}
			next, err := model.NewLease(b.Lease.Holder)
			if err != nil {
				return err
			}
			if next.Continuation, err = model.Nonce(); err != nil {
				return err
			}
			next.Execution, next.RetainedRuns = b.Lease.Execution, b.Lease.RetainedRuns
			b.Lease = next
			result = workspace.Clone(b)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

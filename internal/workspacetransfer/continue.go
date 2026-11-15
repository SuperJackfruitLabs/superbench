package workspacetransfer

import (
	"context"
	"database/sql"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

// Continue resumes only this approved transfer. Source ownership is never
// restored on failure, including failed quota allocation or destination setup.
func (m Manager) Continue(ctx context.Context, sourceKey, id string) (out *Result, operationErr error) {
	if !keyPattern.MatchString(sourceKey) || !keyPattern.MatchString(id) {
		return nil, errors.New("valid workspace transfer ID required")
	}
	ctx, cancel, a, err := m.admin(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	var p Plan
	var j reftransfer.Record
	var current *model.Bench
	err = a.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		var e error
		j, _, e = boundRecord(rows, tx, id)
		if e != nil {
			return e
		}
		p, e = ParsePlan(j.Plan)
		if e != nil {
			return e
		}
		if j.SourceKey != sourceKey || p.Root != m.Store.State || p.DataRoot != m.Store.Data || p.ID != j.ID || p.Digest != j.Digest || p.SourceKey != j.SourceKey || p.SourceID != j.SourceID || p.TargetID != j.TargetID || p.TargetKey != j.TargetKey || p.CreationID != j.CreationID {
			return errors.New("stored transfer approval differs from its authority")
		}
		current = workspace.Clone(rows[j.TargetKey])
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	if j.Phase == "completed" {
		return projection(j, current, false), ctx.Err()
	}
	var token string
	defer func() {
		read := func(grant bool) error {
			return a.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
				latest, _, e := boundRecord(rows, tx, id)
				if e != nil {
					return e
				}
				out = projection(latest, rows[latest.TargetKey], grant)
				return nil
			})
		}
		operationErr = errors.Join(operationErr, read(ctx.Err() == nil))
		if ctxErr := ctx.Err(); ctxErr != nil {
			operationErr = errors.Join(operationErr, ctxErr)
			if token != "" {
				_, e := a.Prep.Stop(j.TargetKey, token, false)
				operationErr = errors.Join(operationErr, e)
			}
			operationErr = errors.Join(operationErr, read(false))
		}
	}()
	if err = relinquish(ctx, a, p); err != nil {
		return nil, err
	}
	// An independently repaired destination may already have reached readiness.
	// Do not re-enter its original initialization helper or adopt a repair grant.
	if current == nil || current.Workspace == nil || current.Workspace.Phase != "ready" {
		spec := workspace.Spec{TransferID: id, Key: p.TargetKey, CheckpointRepository: p.CheckpointRepository, Repositories: p.Repositories, QuotaBytes: p.QuotaBytes}
		current, err = m.Store.StageWorkspaceContext(ctx, p.CreationID, spec)
		if current != nil && current.Lease != nil && current.Workspace != nil && current.Workspace.TransferredFrom != nil && current.Workspace.TransferredFrom.ID == id && current.Workspace.InitializationRepair == nil && current.Lease.Holder == "initialization" && (current.Workspace.Phase == "materializing" || current.Workspace.Phase == "initializing" || current.Workspace.Phase == "prepared") {
			token = current.Lease.Token
		}
		if err != nil {
			return nil, err
		}
		if current.Workspace.Phase != "ready" {
			if current.Lease == nil || token == "" || current.Lease.StopRequested {
				return nil, errors.New("transfer destination initialization is stopped; inspect its explicit repair")
			}
			if current.Workspace.Pending == nil {
				if _, err = a.Prep.Prepare(ctx, p.TargetKey, token, "", false); err != nil {
					return nil, err
				}
			}
			if _, err = a.Lifecycle.FinishInitialization(ctx, p.TargetKey, token); err != nil {
				return nil, err
			}
		}
	}
	err = a.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		latest, _, e := boundRecord(rows, tx, id)
		if e != nil {
			return e
		}
		b := rows[latest.TargetKey]
		if b == nil || b.Workspace == nil || b.Workspace.ID != latest.TargetID || b.Workspace.TransferredFrom == nil || b.Workspace.TransferredFrom.ID != id || b.Workspace.Phase != "ready" || b.Workspace.Pending != nil || b.Lease != nil {
			return errors.New("transfer destination is not ready and unleased")
		}
		if e = a.Lifecycle.Confirm(ctx, b); e != nil {
			return e
		}
		if e = reftransfer.Complete(tx, id, p.Digest); e != nil {
			return e
		}
		return ctx.Err()
	})
	return nil, err
}

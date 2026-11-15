package workspacerepair

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/initrepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func (m Manager) Continue(ctx context.Context, key, id, token string) (out *Result, operationErr error) {
	ctx, err := m.controlled(ctx)
	if err != nil {
		return nil, err
	}
	if !requestID.MatchString(key) || !requestID.MatchString(id) || token == "" {
		return nil, errors.New("initialization repair continuation requires its exact ID and capability")
	}
	var owned *model.Bench
	completed := false
	authorized := false
	defer func() {
		if !authorized {
			return
		}
		current, readErr := m.view(key, id, token, false)
		out = current
		operationErr = errors.Join(operationErr, readErr)
		// The final read is inside the cancellation boundary as well.
		if ctxErr := ctx.Err(); ctxErr != nil {
			_, stopErr := m.Prep.Stop(key, token, false)
			current, readErr = m.view(key, id, token, false)
			out = current
			operationErr = errors.Join(operationErr, ctxErr, stopErr, readErr)
		}
	}()
	err = m.Prep.Gate(key, func() error {
		return m.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b := rows[key]
			if b == nil || b.Workspace == nil || b.Workspace.InitializationRepair == nil || b.Workspace.InitializationRepair.ID != id {
				return errors.New("initialization repair epoch is not current")
			}
			j, err := initrepair.Get(tx, b.Workspace.ID, id)
			if err != nil {
				return err
			}
			if j.Key != key || j.Digest != b.Workspace.InitializationRepair.Digest || subtle.ConstantTimeCompare([]byte(j.OwnerDigest), []byte(hashBytes([]byte(token)))) != 1 {
				return errors.New("initialization repair capability does not own this epoch")
			}
			if j.Status == "completed" && b.Workspace.Phase == "ready" && b.Lease == nil {
				completed = true
				return ctx.Err()
			}
			if j.Status != "active" || b.Lease == nil || subtle.ConstantTimeCompare([]byte(b.Lease.Token), []byte(token)) != 1 || b.Lease.Holder != "initialization" || b.Lease.StopRequested || b.Lease.Continuation == "" || b.Workspace.Phase != "initializing" && b.Workspace.Phase != "prepared" {
				return errors.New("initialization repair was stopped, completed or superseded")
			}
			owned = workspace.Clone(b)
			authorized = true
			return ctx.Err()
		})
	})
	if err != nil {
		return nil, err
	}
	if completed {
		return m.view(key, id, token, false)
	}
	m.Prep.RepairID = id
	if owned.Workspace.Pending == nil {
		if _, err = m.Prep.Prepare(ctx, key, token, "", false); err != nil {
			return nil, err
		}
	}
	l := m.Lifecycle
	l.InitializationRepairID = id
	released := l.Released
	l.Released = func(tx *sql.Tx, b *model.Bench) error {
		j, err := initrepair.Get(tx, b.Workspace.ID, id)
		if err != nil {
			return err
		}
		if j.Status != "active" || j.Digest != b.Workspace.InitializationRepair.Digest || j.OwnerDigest != hashBytes([]byte(token)) {
			return errors.New("initialization repair journal changed before readiness")
		}
		if err = released(tx, b); err != nil {
			return err
		}
		return initrepair.Complete(tx, b.Workspace.ID, id, j.Digest)
	}
	_, err = l.FinishInitialization(ctx, key, token)
	return nil, err
}

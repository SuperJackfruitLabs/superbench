// Package workspaceinit keeps the shared initialization handoff and cancellation
// reconciliation independent of engine storage and runtime configuration.
package workspaceinit

import (
	"context"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

type Manager struct {
	Continue func(context.Context, *model.Bench) (*model.Bench, error)
	Stop     func(key, token string) (*model.Bench, error)
}

// Complete never revives a stopped owner or substitutes a later capability.
// Completed creation replay skips runtime controls and cancellation entirely.
func (m Manager) Complete(ctx context.Context, b *model.Bench) (result *model.Bench, operationErr error) {
	if b.Workspace.Transfer != nil || b.Workspace.TransferredFrom != nil {
		return nil, workspace.ErrTransfer
	}
	if b.Workspace.InitializationRepair != nil {
		return nil, workspace.ErrInitializationRepair
	}
	if b.Workspace.Phase == "ready" || b.Workspace.Archive != nil {
		return b, nil
	}
	key, token := b.Key, b.Lease.Token
	defer func() {
		if ctxErr := ctx.Err(); ctxErr != nil && result != nil && result.Lease != nil {
			stopped, stopErr := m.Stop(key, token)
			if stopped != nil {
				result = stopped
			}
			operationErr = errors.Join(operationErr, ctxErr, stopErr)
		}
	}()
	return m.Continue(ctx, b)
}

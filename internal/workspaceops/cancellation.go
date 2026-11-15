package workspaceops

import (
	"context"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

// Reconcile cancellation across acquisition/preparation/publication boundaries,
// including errors before a backend has entered its own cancellation monitor.
func (m Manager) reconcileCanceledWork(ctx context.Context, r protocolv2.Request, token, grant string, out **Result, operationErr *error) {
	if ctx.Err() == nil {
		return
	}
	*operationErr = errors.Join(*operationErr, ctx.Err())
	if m.Store.CheckWorkspaceOwner(r.Key, token, r.Holder) != nil {
		return
	}
	stopped, err := m.Store.StopWorkspace(r.Key, token, false)
	*operationErr = errors.Join(*operationErr, err)
	if stopped == nil {
		return
	}
	current := result(stopped, grant, true)
	if prior := *out; prior != nil {
		current.Execution, current.ExitCode, current.LogTruncated = prior.Execution, prior.ExitCode, prior.LogTruncated
	}
	selectPreparation(current, r)
	*out = current
}

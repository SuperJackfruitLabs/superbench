package workspacetransfer

import (
	"context"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
)

// ContinueWithAdminForTest exercises the public continuation body with controlled
// transaction boundaries, without adding runtime injection hooks.
func (m Manager) ContinueWithAdminForTest(ctx context.Context, source, id string, a workspacerepair.Manager, missing bool) (*Result, error) {
	if missing {
		return m.continueMissingAuthorized(ctx, source, id, a)
	}
	return m.continueAuthorized(ctx, source, id, a)
}

// RelinquishMissingForTest separates durable retirement from destination staging
// so external tests can inject source changes at exact managed Git boundaries.
func (m Manager) RelinquishMissingForTest(ctx context.Context, a workspacerepair.Manager, p MissingPlan) error {
	return m.relinquishMissing(ctx, a, p)
}

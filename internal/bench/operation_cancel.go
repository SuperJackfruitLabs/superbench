package bench

import (
	"context"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcancel"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

// ReconcileOperationCancellation requires the durable service cancel barrier
// and its complete certified helper inventory. It returns no writer capability.
func (s Store) ReconcileOperationCancellation(ctx context.Context, i operationcontrol.Intent, helpers []operationcontrol.Helper) error {
	m, err := operationcancel.Persistent(s.State, s.Data, s.backend(), stageexec.Gate)
	if err != nil {
		return err
	}
	return m.Reconcile(ctx, i, helpers)
}

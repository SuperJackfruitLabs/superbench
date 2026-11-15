package bench

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcancel"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

// OperationCancellation returns current-schema-only local reconciliation. Its
// caller must supply the exact durable receipt barrier or finalization cutoff.
func (s Store) OperationCancellation() (operationcancel.Manager, error) {
	return operationcancel.Persistent(s.State, s.Data, s.backend(), stageexec.Gate)
}

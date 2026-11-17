// Package scalarapi projects v1 (scalar bench) operation errors onto the static
// codes and messages the v1 protocol returns, as workspaceapi does for v2.
// Private causes never reach a client; benchd logs them (internal/failurelog).
package scalarapi

import (
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
)

// ErrFreshCreate refuses v1 create and recover while fresh_payload is active.
// That needs the indexed quota journal, whose helper refuses the unscoped bench
// allocation a scalar create makes, so the create would fail after recording
// its intent. benchd refuses before any effect instead.
var ErrFreshCreate = errors.New("scalar create and recover are refused while fresh_payload is active; create a v2 workspace")

// ErrorStatus keeps v1's error set: capacity exhaustion has no v1 code and
// stays operation_failed.
func ErrorStatus(e error) (int, string, string) {
	switch {
	case errors.Is(e, ErrFreshCreate):
		return 409, "conflict", "scalar benches cannot be created while fresh_payload is active; use the workspace API"
	case errors.Is(e, bench.ErrNotFound):
		return 404, "not_found", "unknown bench"
	case errors.Is(e, bench.ErrLease):
		return 403, "forbidden", "valid holder capability required"
	case errors.Is(e, bench.ErrWorkspace):
		return 409, "conflict", "aggregate workspace requires the workspace API"
	case errors.Is(e, bench.ErrArchived) || errors.Is(e, bench.ErrBusy) || errors.Is(e, bench.ErrExists) || errors.Is(e, bench.ErrNotFenced) || errors.Is(e, bench.ErrCheckpoint) || errors.Is(e, bench.ErrDirty):
		return 409, "conflict", "bench state conflicts with this operation"
	}
	return 422, "operation_failed", "operation failed; inspect retained bench state and local logs"
}

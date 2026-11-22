package workspaceapi

import (
	"context"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceops"
)

// ErrorStatus exposes static messages and typed logical capacity quantities;
// private backend diagnostics stay in
// local evidence instead of becoming an API error or a printed capability.
func ErrorStatus(err error) (int, string, string) {
	switch {
	case err == nil:
		return 200, "", ""
	case errors.Is(err, workspaceops.ErrInvalid):
		return 400, "invalid_request", "invalid workspace operation"
	case errors.Is(err, workspaceops.ErrUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return 503, "unavailable", "workspace operation unavailable; inspect retained state"
	case errors.Is(err, bench.ErrNotFound):
		return 404, "not_found", "unknown bench"
	case errors.Is(err, bench.ErrLease):
		return 403, "forbidden", "valid holder capability required"
	case errors.Is(err, bench.ErrDirty): // the one conflict the holder can fix in the tree
		return 409, "conflict", "workspace has uncommitted or untracked files; commit or remove them before checkpoint or release"
	case errors.Is(err, workspaceops.ErrConflict), errors.Is(err, bench.ErrScalar), errors.Is(err, bench.ErrWorkspace), errors.Is(err, bench.ErrArchived), errors.Is(err, bench.ErrBusy), errors.Is(err, bench.ErrExists), errors.Is(err, bench.ErrNotFenced), errors.Is(err, bench.ErrCheckpoint):
		return 409, "conflict", "workspace state conflicts with this operation"
	}
	var capacity *bench.CapacityError
	if errors.As(err, &capacity) && capacity.Valid() {
		return 422, "capacity_exhausted", capacity.Message()
	}
	return 422, "operation_failed", "operation failed; inspect retained workspace state and local logs"
}

// Envelope verifies the complete projected response before it can reach stdout
// or a receipt. It never serializes a private Bench or execution record.
func Envelope(r protocolv2.Request, out protocolv2.Result, proof protocolv2.Authority, operationErr error) (protocolv2.Response, int, error) {
	status, code, message := ErrorStatus(operationErr)
	response := protocolv2.Response{Version: protocolv2.Version, Operation: r.Operation, RequestID: r.RequestID, OK: operationErr == nil, Result: out}
	if operationErr != nil {
		response.Error = &protocolv2.APIError{Code: code, Message: message}
	}
	if err := protocolv2.ValidateResponse(response, status, r); err != nil {
		return protocolv2.Response{}, 0, err
	}
	if err := protocolv2.ValidateAuthority(response, proof); err != nil {
		return protocolv2.Response{}, 0, err
	}
	return response, status, nil
}

// Execute is the local administrative crossing; HTTP separately authenticates
// its Unix peer and reserves receipts before invoking the same operations layer.
func Execute(ctx context.Context, store bench.Store, r protocolv2.Request, uid uint32) (protocolv2.Response, int, error) {
	var result protocolv2.Result
	var proof protocolv2.Authority
	var operationErr error
	switch r.Operation {
	case "status":
		var rows []bench.Bench
		rows, operationErr = store.Snapshot(r.Key)
		if operationErr == nil {
			status := &protocolv2.Status{Benches: make([]protocolv2.Inventory, 0, len(rows))}
			proof.InventoryKeys = make([]string, 0, len(rows))
			for i := range rows {
				status.Benches = append(status.Benches, Inventory(&rows[i]))
				proof.InventoryKeys = append(proof.InventoryKeys, rows[i].Key)
			}
			result = status
		}
	case "host_status":
		h, err := store.HostStatus()
		operationErr = err
		if err == nil {
			result = &protocolv2.HostStatus{Managed: h.Managed, DataFreeBytes: h.Data.Free, StateFreeBytes: h.State.Free}
		}
	default:
		out, err := (workspaceops.Manager{Store: store}).Execute(ctx, r, uid)
		var projectionErr error
		result, proof, projectionErr = Operation(r, out, err)
		operationErr = errors.Join(err, projectionErr)
	}
	return Envelope(r, result, proof, operationErr)
}

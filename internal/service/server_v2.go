package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceapi"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceops"
)

func (s *Server) serveV2(w http.ResponseWriter, r *http.Request) {
	s.admission.Lock()
	if s.closing {
		s.admission.Unlock()
		s.replyV2(w, protocolv2.Request{}, nil, 503, "unavailable", "service is shutting down")
		return
	}
	s.handlers.Add(1)
	s.admission.Unlock()
	defer s.handlers.Done()
	uid, ok := r.Context().Value(uidKey{}).(uint32)
	if !ok || (!s.config.Operators[uid] && !s.config.Readers[uid]) {
		s.replyV2(w, protocolv2.Request{}, nil, 403, "forbidden", "Unix peer is not authorized")
		return
	}
	if r.URL.Path != "/v2/operations" {
		s.replyV2(w, protocolv2.Request{}, nil, 404, "not_found", "unknown endpoint")
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		s.replyV2(w, protocolv2.Request{}, nil, 405, "invalid_request", "POST required")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		s.replyV2(w, protocolv2.Request{}, nil, 415, "invalid_request", "JSON required")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, protocolv2.MaxBody))
	if err != nil {
		s.replyV2(w, protocolv2.Request{}, nil, 413, "invalid_request", "request body exceeds limit")
		return
	}
	if s.proposedCancellation {
		if cancellation, parseErr := protocolv2.ParseCancelRequest(body); parseErr == nil {
			s.cancelV2(w, uid, cancellation)
			return
		}
	}
	request, err := protocolv2.Parse(body)
	if err != nil {
		s.replyV2(w, protocolv2.Request{}, nil, 400, "invalid_request", "invalid workspace operation request")
		return
	}
	if !protocolv2.ReadOnly(request.Operation) && !s.config.Operators[uid] {
		s.replyV2(w, request, nil, 403, "forbidden", "operator authority required")
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		s.replyV2(w, request, nil, 503, "unavailable", "active operation limit reached")
		return
	}
	dispatchContext := s.ctx
	var intent operationcontrol.Intent
	manager := workspaceops.Manager{Store: s.config.Store}
	if !protocolv2.Growth(request.Operation) {
		if err := manager.Preflight(request); err != nil {
			status, code, message := operationErrorV2(err)
			s.replyV2(w, request, nil, status, code, message)
			return
		}
	}
	if protocolv2.Growth(request.Operation) {
		digest, err := request.Digest()
		if err != nil {
			s.replyV2(w, request, nil, 400, "invalid_request", "invalid workspace operation request")
			return
		}
		prior, found, lookupErr := s.receipts.Lookup(uid, request.RequestID, digest)
		if found || lookupErr != nil {
			s.replayV2(w, request, prior, lookupErr)
			return
		}
		if request.Operation == "create" || request.Operation == "recover" {
			identity, identityErr := workspaceops.CreationID(request, uid)
			if identityErr != nil {
				s.replyV2(w, request, nil, 400, "invalid_request", "invalid creation identity")
				return
			}
			rows, snapshotErr := s.config.Store.Snapshot(request.Key)
			if snapshotErr != nil && !errors.Is(snapshotErr, bench.ErrNotFound) {
				status, code, message := operationErrorV2(snapshotErr)
				s.replyV2(w, request, nil, status, code, message)
				return
			}
			if len(rows) == 1 && rows[0].Workspace != nil && rows[0].Workspace.CreationID == identity {
				// Another dispatch may have claimed the receipt after our initial lookup.
				prior, found, lookupErr = s.receipts.Lookup(uid, request.RequestID, digest)
				if found || lookupErr != nil {
					s.replayV2(w, request, prior, lookupErr)
					return
				}
				s.replyV2(w, request, nil, 409, "operation_in_doubt", "creation receipt missing; inspect retained workspace state")
				return
			}
		}
		if err := manager.Preflight(request); err != nil {
			// A concurrent same-ID request may have completed while preflight observed
			// its changed owner. Recheck before reporting the mutable state conflict.
			prior, found, lookupErr = s.receipts.Lookup(uid, request.RequestID, digest)
			if found || lookupErr != nil {
				s.replayV2(w, request, prior, lookupErr)
				return
			}
			status, code, message := operationErrorV2(err)
			s.replyV2(w, request, nil, status, code, message)
			return
		}
		var cached *receipt
		var deferStop func()
		defer func() {
			if deferStop != nil {
				deferStop()
			}
		}()
		err = s.config.Store.WithWorkspace(request.Key, request.Operation == "create" || request.Operation == "recover", func() error {
			var claimErr error
			if s.proposedCancellation {
				intent, claimErr = newOperationIntent(request, uid, digest)
				if claimErr != nil {
					return claimErr
				}
				var stop func()
				cached, dispatchContext, stop, claimErr = s.receipts.ClaimScope(s.ctx, intent)
				// No controller is attached for historical replay or admission failure.
				if stop != nil {
					deferStop = stop
				}
			} else {
				cached, claimErr = s.receipts.Claim(uid, request.RequestID, digest)
			}
			return claimErr
		})
		if err != nil {
			status, code, message := receiptErrorV2(err)
			s.replyV2(w, request, nil, status, code, message)
			return
		}
		if cached != nil {
			s.replayV2(w, request, cached, nil)
			return
		}
	}
	result, proof, operationErr := s.dispatchV2(dispatchContext, request, uid)
	status, code, message := 200, "", ""
	if operationErr != nil {
		status, code, message = operationErrorV2(operationErr)
	}
	response := responseV2(request, result, code, message)
	encoded, encodeErr := json.Marshal(response)
	if encodeErr == nil {
		encodeErr = protocolv2.ValidateResponse(response, status, request)
	}
	if encodeErr == nil {
		encodeErr = protocolv2.ValidateAuthority(response, proof)
	}
	if encodeErr != nil {
		// Effects may already be durable. Store a bounded ambiguous outcome instead
		// of making the original request executable again or exposing private data.
		status, code, message = 503, "operation_in_doubt", "response requires local reconciliation"
		if protocolv2.ReadOnly(request.Operation) {
			code, message = "unavailable", "complete response unavailable; use a specific workspace key"
		}
		encoded, _ = json.Marshal(responseV2(request, nil, code, message))
	}
	if protocolv2.Growth(request.Operation) {
		if err := s.receipts.Finish(uid, request.RequestID, encoded, status); err != nil {
			if encodeErr != nil || errors.Is(err, errCancelRequested) {
				result = nil
			}
			s.replyV2(w, request, result, 503, "operation_in_doubt", "receipt save failed; inspect retained workspace state")
			return
		}
	}
	if s.proposedCancellation && protocolv2.Growth(request.Operation) {
		// The historical response commit is the cutoff, never HTTP delivery.
		if err := s.finalizeOperation(intent); err != nil {
			s.wakeCancellation()
		}
	}
	s.raw(w, status, encoded)
}

func (s *Server) dispatchV2(ctx context.Context, r protocolv2.Request, uid uint32) (protocolv2.Result, protocolv2.Authority, error) {
	proof := protocolv2.Authority{}
	switch r.Operation {
	case "status":
		rows, err := s.config.Store.Snapshot(r.Key)
		if err != nil {
			return nil, proof, err
		}
		result := &protocolv2.Status{Benches: make([]protocolv2.Inventory, 0, len(rows))}
		proof.InventoryKeys = make([]string, 0, len(rows))
		for i := range rows {
			result.Benches = append(result.Benches, workspaceapi.Inventory(&rows[i]))
			proof.InventoryKeys = append(proof.InventoryKeys, rows[i].Key)
		}
		return result, proof, nil
	case "host_status":
		h, err := s.config.Store.HostStatus()
		if err != nil {
			return nil, proof, err
		}
		return &protocolv2.HostStatus{Managed: h.Managed, DataFreeBytes: h.Data.Free, StateFreeBytes: h.State.Free}, proof, nil
	}
	out, err := (workspaceops.Manager{Store: s.config.Store}).Execute(ctx, r, uid)
	result, proof, projectionErr := workspaceapi.Operation(r, out, err)
	return result, proof, errors.Join(err, projectionErr)
}

func operationErrorV2(err error) (int, string, string) {
	return workspaceapi.ErrorStatus(err)
}

func receiptErrorV2(err error) (int, string, string) {
	switch {
	case errors.Is(err, errConflict):
		return 409, "conflict", "request id was used with another body"
	case errors.Is(err, errInProgress):
		return 409, "in_progress", "operation is still running"
	case errors.Is(err, errInDoubt):
		return 409, "operation_in_doubt", "inspect workspace state before using another request id"
	case errors.Is(err, errCapacity):
		return 503, "unavailable", "receipt retention budget exhausted"
	case errors.Is(err, bench.ErrScalar), errors.Is(err, bench.ErrNotFound):
		return operationErrorV2(err)
	}
	return 503, "unavailable", "receipt storage unavailable; retry the same request id"
}

func responseV2(r protocolv2.Request, result protocolv2.Result, code, message string) protocolv2.Response {
	op := r.Operation
	if !protocolv2.Known(op) {
		op = ""
	}
	response := protocolv2.Response{Version: protocolv2.Version, Operation: op, RequestID: r.RequestID, OK: code == "", Result: result}
	if code != "" {
		response.Error = &protocolv2.APIError{Code: code, Message: message}
	}
	return response
}

func (s *Server) replyV2(w http.ResponseWriter, r protocolv2.Request, result protocolv2.Result, status int, code, message string) {
	response := responseV2(r, result, code, message)
	if protocolv2.ValidateResponse(response, status, r) != nil {
		response.Result = nil
		response.Error = &protocolv2.APIError{Code: "operation_in_doubt", Message: "response requires local reconciliation"}
		status = 503
	}
	encoded, _ := json.Marshal(response)
	s.raw(w, status, encoded)
}

func (s *Server) replayV2(w http.ResponseWriter, request protocolv2.Request, cached *receipt, err error) {
	if err != nil {
		status, code, message := receiptErrorV2(err)
		s.replyV2(w, request, nil, status, code, message)
		return
	}
	if cached == nil {
		s.replyV2(w, request, nil, 503, "operation_in_doubt", "stored response unavailable")
		return
	}
	if _, err := protocolv2.ParseResponse(cached.Bytes(), cached.HTTPStatus(), request); err != nil {
		s.replyV2(w, request, nil, 503, "operation_in_doubt", "stored response requires local reconciliation")
		return
	}
	s.raw(w, cached.HTTPStatus(), cached.Bytes())
}

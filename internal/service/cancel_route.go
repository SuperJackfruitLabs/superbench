package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

// This boundary is private: neither production Config nor Serve enables it.
// Admission persists intent only; independent reconciliation proves completion.
func (s *Server) cancelV2(w http.ResponseWriter, uid uint32, request protocolv2.CancelRequest) {
	response := protocolv2.CancelResponse{Version: protocolv2.Version, Operation: "cancel"}
	status := 200
	if !s.config.Operators[uid] {
		status = 403
		response.Error = &protocolv2.APIError{Code: "forbidden", Message: "operator authority required"}
	} else {
		state, err := s.receipts.RequestCancel(uid, request.TargetRequestID, request.TargetDigest)
		if err != nil {
			status, code, message := receiptErrorV2(err)
			response.Error = &protocolv2.APIError{Code: code, Message: message}
			s.writeCancel(w, status, request, response)
			return
		}
		response.OK = true
		response.Result = &protocolv2.CancelObservation{TargetRequestID: request.TargetRequestID, TargetDigest: request.TargetDigest, State: state}
		s.wakeCancellation()
	}
	s.writeCancel(w, status, request, response)
}

func (s *Server) writeCancel(w http.ResponseWriter, status int, request protocolv2.CancelRequest, response protocolv2.CancelResponse) {
	if protocolv2.ValidateCancelResponse(response, status, request) != nil {
		status = 503
		response = protocolv2.CancelResponse{Version: protocolv2.Version, Operation: "cancel", Error: &protocolv2.APIError{Code: "operation_in_doubt", Message: "cancellation requires local reconciliation"}}
	}
	raw, _ := json.Marshal(response)
	s.raw(w, status, raw)
}
func (s *Server) wakeCancellation() {
	select {
	case s.cancelWake <- struct{}{}:
	default:
	}
}

func newOperationIntent(request protocolv2.Request, uid uint32, digest string) (operationcontrol.Intent, error) {
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		return operationcontrol.Intent{}, err
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: uid, RequestID: request.RequestID, Digest: digest}, Key: request.Key, Operation: request.Operation, Epoch: hex.EncodeToString(epoch[:])}
	if !i.Valid() {
		return i, errors.New("invalid operation intent")
	}
	return i, nil
}

// Only the private listener (cancellation or receipt lookup) has connection classes.
func (s *Server) promoteNormal(r *http.Request) error {
	if !s.proposedCancellation && !s.proposedReceiptLookup {
		return nil
	}
	c, _ := r.Context().Value(connectionKey{}).(*peerConn)
	if c == nil {
		return nil
	} // In-process handler tests have no transport reservation.
	return c.promote()
}

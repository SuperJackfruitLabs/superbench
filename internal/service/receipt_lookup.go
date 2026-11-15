package service

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

// receiptLookupV2 reads only same-peer historical evidence. Public Serve keeps
// the proposed extension disabled; no request body can enable it.
func (s *Server) receiptLookupV2(w http.ResponseWriter, r *http.Request, uid uint32) {
	var target protocolv2.ReceiptLookupRequest
	fail := func(status int, code string) { s.replyReceiptLookup(w, target, nil, status, code) }
	if !s.config.Operators[uid] {
		fail(403, "forbidden")
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		fail(405, "invalid_request")
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		fail(415, "invalid_request")
		return
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, protocolv2.MaxBody))
	if e != nil {
		fail(413, "invalid_request")
		return
	}
	target, e = protocolv2.ParseReceiptLookupRequest(raw)
	if e != nil {
		fail(400, "invalid_request")
		return
	}
	// Dedicated transport promotion frees classification/cancellation admission
	// before a ledger read. Excess reads fail rather than queue or use normal slots.
	if c, ok := r.Context().Value(connectionKey{}).(*peerConn); ok && c.promoteRead() != nil {
		fail(503, "unavailable")
		return
	}
	if s.receiptSlots == nil {
		fail(503, "unavailable")
		return
	}
	select {
	case s.receiptSlots <- struct{}{}:
		defer func() { <-s.receiptSlots }()
	default:
		fail(503, "unavailable")
		return
	}
	if s.receipts == nil {
		fail(503, "unavailable")
		return
	}
	prior, found, e := s.receipts.Lookup(uid, target.TargetRequestID, target.TargetDigest)
	result := &protocolv2.ReceiptLookupResult{TargetRequestID: target.TargetRequestID, TargetDigest: target.TargetDigest}
	switch {
	case errors.Is(e, errConflict):
		fail(409, "conflict")
		return
	case errors.Is(e, errInProgress):
		result.State = "in_progress"
	case errors.Is(e, errInDoubt):
		result.State = "operation_in_doubt"
	case errors.Is(e, errCancelRequested):
		result.State = "cancel_requested"
	case e != nil:
		fail(503, "unavailable")
		return
	case !found:
		result.State = "absent"
	case prior == nil:
		fail(503, "unavailable")
		return
	default:
		body := prior.Bytes()
		if len(body) == 0 || len(body) > protocolv2.MaxBody || !utf8.Valid(body) || prior.HTTPStatus() < 200 || prior.HTTPStatus() > 599 {
			fail(503, "unavailable")
			return
		}
		result.State = "completed"
		result.HTTPStatus = prior.HTTPStatus()
		result.ResponseBody = string(body)
	}
	s.replyReceiptLookup(w, target, result, 200, "")
}
func (s *Server) replyReceiptLookup(w http.ResponseWriter, target protocolv2.ReceiptLookupRequest, result *protocolv2.ReceiptLookupResult, status int, code string) {
	response := protocolv2.ReceiptLookupResponse{Version: protocolv2.Version, Operation: "receipt_lookup", OK: code == "", Result: result}
	if code != "" {
		response.Error = &protocolv2.APIError{Code: code, Message: "receipt lookup refused"}
	}
	raw, e := json.Marshal(response)
	if e != nil || len(raw) > protocolv2.MaxReceiptLookupResponse {
		status = 503
		raw = []byte(`{"version":"superbench.v2","operation":"receipt_lookup","ok":false,"error":{"code":"unavailable","message":"receipt lookup refused"}}`)
	} else if result != nil && protocolv2.ValidateReceiptLookupResponse(response, status, target) != nil {
		status = 503
		raw = []byte(`{"version":"superbench.v2","operation":"receipt_lookup","ok":false,"error":{"code":"unavailable","message":"receipt lookup refused"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

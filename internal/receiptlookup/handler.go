// Package receiptlookup serves the proposed read-only v2 receipt lookup. It
// reads only same-peer historical evidence and never claims, executes, renews
// or resends an operation. The service owns peer authentication and admission.
package receiptlookup

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationbody"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationreceipts"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

// Handler is constructed per request by the service. Promote moves a promoted
// connection to its dedicated read budget; nil means no promotion is needed.
// Finite is the default-off finite-I/O gate: only with it, and only for the
// explicit profile, may an original body up to 2 MiB be returned.
type Handler struct {
	Operators map[uint32]bool
	Ledger    *operationreceipts.Ledger
	Slots     chan struct{}
	Promote   func() error
	Finite    bool
}

const refused = `{"version":"superbench.v2","operation":"receipt_lookup","ok":false,"error":{"code":"unavailable","message":"receipt lookup refused"}}`

func (h Handler) Serve(w http.ResponseWriter, r *http.Request, uid uint32) {
	var target protocolv2.ReceiptLookupRequest
	finite := false
	fail := func(status int, code string) { reply(w, target, nil, status, code, finite) }
	if !h.Operators[uid] {
		fail(403, "forbidden")
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		fail(405, "invalid_request")
		return
	}
	finite, ok := operationbody.Profiled(r, h.Finite)
	if !ok {
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
	if h.Promote != nil && h.Promote() != nil {
		fail(503, "unavailable")
		return
	}
	if h.Slots == nil {
		fail(503, "unavailable")
		return
	}
	select {
	case h.Slots <- struct{}{}:
		defer func() { <-h.Slots }()
	default:
		fail(503, "unavailable")
		return
	}
	if h.Ledger == nil {
		fail(503, "unavailable")
		return
	}
	prior, found, e := h.Ledger.Lookup(uid, target.TargetRequestID, target.TargetDigest)
	result := &protocolv2.ReceiptLookupResult{TargetRequestID: target.TargetRequestID, TargetDigest: target.TargetDigest}
	switch {
	case errors.Is(e, operationreceipts.ErrConflict):
		fail(409, "conflict")
		return
	case errors.Is(e, operationreceipts.ErrInProgress):
		result.State = "in_progress"
	case errors.Is(e, operationreceipts.ErrInDoubt):
		result.State = "operation_in_doubt"
	case errors.Is(e, operationreceipts.ErrCancelRequested):
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
		// An unprofiled lookup reports a larger retained body unavailable; it
		// never truncates it. Only the profile admits the 2 MiB finite bound.
		limit, _ := protocolv2.ReceiptLookupLimits(finite)
		if len(body) == 0 || len(body) > limit || !utf8.Valid(body) || prior.HTTPStatus() < 200 || prior.HTTPStatus() > 599 {
			fail(503, "unavailable")
			return
		}
		result.State = "completed"
		result.HTTPStatus = prior.HTTPStatus()
		result.ResponseBody = string(body)
	}
	reply(w, target, result, 200, "", finite)
}

func reply(w http.ResponseWriter, target protocolv2.ReceiptLookupRequest, result *protocolv2.ReceiptLookupResult, status int, code string, finite bool) {
	response := protocolv2.ReceiptLookupResponse{Version: protocolv2.Version, Operation: "receipt_lookup", OK: code == "", Result: result}
	if code != "" {
		response.Error = &protocolv2.APIError{Code: code, Message: "receipt lookup refused"}
	}
	_, envelope := protocolv2.ReceiptLookupLimits(finite)
	raw, e := json.Marshal(response)
	if e != nil || len(raw) > envelope {
		status = 503
		raw = []byte(refused)
	} else if result != nil && protocolv2.ValidateReceiptLookupResponseProfile(response, status, target, finite) != nil {
		status = 503
		raw = []byte(refused)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

package registrationobserve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

// HTTPHandler joins the actual service peer and connection admission to the
// feature-owned installed authority checks. Admit only reserves the existing
// normal connection; it cannot provide policy, helper or operation authority.
// The service keeps this handler behind its private, default-off route gate.
type HTTPHandler struct {
	Context   context.Context
	Store     bench.Store
	Operators map[uint32]bool
	Slots     chan struct{}
	Admit     func(*http.Request) error
}

func (h HTTPHandler) Serve(w http.ResponseWriter, r *http.Request, uid uint32) {
	var request protocolv2.RegistrationObserveRequest
	fail := func(status int, code string) { replyRegistrationObservation(w, request, nil, status, code) }
	if !h.Operators[uid] {
		fail(403, "forbidden")
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		fail(405, "invalid_request")
		return
	}
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || len(params) != 0 {
		fail(415, "invalid_request")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, protocolv2.MaxBody))
	if err != nil {
		fail(413, "invalid_request")
		return
	}
	request, err = protocolv2.ParseRegistrationObserveRequest(raw)
	if err != nil {
		fail(400, "invalid_request")
		return
	}
	if h.Context == nil || h.Admit == nil || h.Admit(r) != nil {
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
	// Keep publication bounded while the real shared owner lock remains held.
	// A transport without deadline support cannot open that observation.
	if err = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		fail(503, "unavailable")
		return
	}
	// Resolve only the actual fixed installed registry/process. Neither a DTO
	// nor a test-injected callback can supply authority or a helper implementation.
	authority, err := serviceidentity.ResolveService()
	if err != nil {
		fail(503, "unavailable")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(h.Context, cancel)
	defer stop()
	observation, err := Observe(ctx, h.Store, authority, Request{Key: request.Key, BenchID: request.BenchID, Holder: request.Holder, LeaseToken: request.LeaseToken})
	if err != nil {
		switch {
		case errors.Is(err, bench.ErrNotFound):
			fail(404, "not_found")
		case errors.Is(err, bench.ErrLease):
			fail(409, "conflict")
		default:
			fail(503, "unavailable")
		}
		return
	}
	defer observation.Close()
	result, err := observation.Result()
	if err != nil {
		fail(503, "unavailable")
		return
	}
	replyRegistrationObservation(w, request, Projection(result), 200, "")
}

func replyRegistrationObservation(w http.ResponseWriter, request protocolv2.RegistrationObserveRequest, result *protocolv2.RegistrationObservation, status int, code string) {
	response := protocolv2.RegistrationObserveResponse{Version: protocolv2.Version, Operation: "registration_observe", OK: code == "", Result: result}
	if code != "" {
		response.Error = &protocolv2.APIError{Code: code, Message: "private registration observation refused"}
	}
	raw, err := json.Marshal(response)
	if err != nil || len(raw) > protocolv2.MaxBody || protocolv2.ValidateRegistrationObserveResponse(response, status, request) != nil {
		status = 503
		raw = []byte(`{"version":"superbench.v2","operation":"registration_observe","ok":false,"error":{"code":"unavailable","message":"private registration observation refused"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

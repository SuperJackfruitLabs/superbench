// Package remoteapi serves benchd's /v2/remote/* socket routes: a writer
// certificate for an operator holding the lease token, and remote status for
// operators and readers. The service owns peer authentication; this handler
// owns authorization by uid. A lease token read here is passed only to
// remotestate.Grant and never written to a reply, a log or an audit row.
package remoteapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteproto"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteschema"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
)

// StatusLimit bounds the audit rows remote status returns.
const StatusLimit = 50

type Handler struct {
	Authority          *remoteca.Authority
	State              func() (remotestate.Manager, error)
	Address            string
	Operators, Readers map[uint32]bool
	Extend             func(fingerprint string, c remoteca.Claims, until time.Time) // registry hook
	Now                func() time.Time
}

// Serve routes one request. admit, when set, reserves the connection's normal
// admission after the request is authorized and parsed; a refusal is 503.
func (h Handler) Serve(w http.ResponseWriter, r *http.Request, uid uint32, admit func(*http.Request) error) {
	switch r.URL.Path {
	case "/v2/remote/credentials":
		h.credential(w, r, uid, admit)
	case "/v2/remote/status":
		h.status(w, r, uid, admit)
	default:
		reply(w, "", 404, nil, "not_found", "unknown endpoint")
	}
}

// read checks the method and media type and reads at most MaxBody bytes. It
// replies itself and returns false on a refusal.
func read(w http.ResponseWriter, r *http.Request, operation string) ([]byte, bool) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		reply(w, operation, 405, nil, "invalid_request", "POST required")
		return nil, false
	}
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
		reply(w, operation, 415, nil, "invalid_request", "application/json required")
		return nil, false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, remoteproto.MaxBody))
	if err != nil {
		reply(w, operation, 413, nil, "invalid_request", "request body too large")
		return nil, false
	}
	return raw, true
}

func (h Handler) now() time.Time {
	if h.Now == nil {
		return time.Now().UTC()
	}
	return h.Now().UTC()
}

func (h Handler) state(w http.ResponseWriter, r *http.Request, operation string, admit func(*http.Request) error) (remotestate.Manager, bool) {
	if admit != nil && admit(r) != nil {
		reply(w, operation, 503, nil, "unavailable", "connection limit reached")
		return remotestate.Manager{}, false
	}
	if h.Authority == nil || h.State == nil {
		reply(w, operation, 503, nil, "unavailable", "remote access unavailable")
		return remotestate.Manager{}, false
	}
	m, err := h.State()
	if err != nil {
		reply(w, operation, 503, nil, "unavailable", "remote access unavailable")
		return remotestate.Manager{}, false
	}
	return m, true
}

func (h Handler) credential(w http.ResponseWriter, r *http.Request, uid uint32, admit func(*http.Request) error) {
	const op = remoteproto.OperationCredential
	if !h.Operators[uid] {
		reply(w, op, 403, nil, "forbidden", "operator authority required")
		return
	}
	raw, ok := read(w, r, op)
	if !ok {
		return
	}
	req, err := remoteproto.ParseCredentialRequest(raw)
	if err != nil {
		reply(w, op, 400, nil, "invalid_request", "invalid remote credential request")
		return
	}
	fingerprint, err := remoteca.Fingerprint(req.PublicKey)
	if err != nil {
		reply(w, op, 400, nil, "invalid_request", "public key refused")
		return
	}
	m, ok := h.state(w, r, op, admit)
	if !ok {
		return
	}
	g, err := m.Grant(r.Context(), req.Key, req.LeaseToken)
	if err != nil {
		refuse(w, op, err)
		return
	}
	now := h.now()
	issued, err := h.Authority.IssueWriter(req.PublicKey, g.Claims, now)
	if errors.Is(err, remoteca.ErrKey) {
		reply(w, op, 400, nil, "invalid_request", "public key refused")
		return
	}
	if err != nil {
		reply(w, op, 503, nil, "unavailable", "certificate not issued")
		return
	}
	c := g.Claims
	record := remoteschema.CertificateRecord{Serial: issued.Serial, Key: g.Key, BenchID: c.BenchID, LeaseID: c.LeaseID, Generation: c.Generation, ExecutionID: c.ExecutionID, Holder: g.Holder, Fingerprint: fingerprint, IssuedAt: now, ValidBefore: issued.ValidBefore}
	if err := m.RecordCertificate(record); err != nil {
		reply(w, op, 503, nil, "unavailable", "certificate not recorded")
		return
	}
	if h.Extend != nil {
		h.Extend(fingerprint, c, issued.ValidBefore)
	}
	// Built field by field: a composite literal of a type named Credential
	// reads as a process credential switch to the architecture pin.
	var out remoteproto.Credential
	out.Certificate, out.User, out.Address = issued.Line, remoteca.Principal(c.BenchID), h.Address
	out.HostKey, out.HostKeyFingerprint = h.Authority.HostKeyLine(), h.Authority.HostFingerprint()
	out.LeaseID, out.Generation, out.ValidBefore = c.LeaseID, c.Generation, stamp(issued.ValidBefore)
	out.WorkingDirectory = g.WorkingDirectory
	reply(w, op, 200, out, "", "")
}

// refuse maps a grant refusal to a static reply. Anything unrecognised is
// unavailable, never a success.
func refuse(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, bench.ErrNotFound):
		reply(w, op, 404, nil, "not_found", "unknown workspace")
	case errors.Is(err, bench.ErrLease):
		reply(w, op, 403, nil, "forbidden", "lease token does not own the workspace lease")
	case errors.Is(err, remotestate.ErrUnavailable):
		reply(w, op, 503, nil, "unavailable", "remote access unavailable")
	case errors.Is(err, remotestate.ErrConflict):
		reply(w, op, 409, nil, "conflict", "lease is not usable for remote work")
	default:
		reply(w, op, 503, nil, "unavailable", "remote access unavailable")
	}
}

func (h Handler) status(w http.ResponseWriter, r *http.Request, uid uint32, admit func(*http.Request) error) {
	const op = remoteproto.OperationStatus
	if !h.Operators[uid] && !h.Readers[uid] {
		reply(w, op, 403, nil, "forbidden", "Unix peer is not authorized")
		return
	}
	raw, ok := read(w, r, op)
	if !ok {
		return
	}
	req, err := remoteproto.ParseStatusRequest(raw)
	if err != nil {
		reply(w, op, 400, nil, "invalid_request", "invalid remote status request")
		return
	}
	m, ok := h.state(w, r, op, admit)
	if !ok {
		return
	}
	s, err := m.Status(req.Key, StatusLimit)
	if err != nil {
		reply(w, op, 503, nil, "unavailable", "remote status unavailable")
		return
	}
	out := remoteproto.Status{Address: h.Address, HostKeyFingerprint: h.Authority.HostFingerprint(), Remote: []remoteproto.RemoteExecution{}, Certificates: []remoteproto.Certificate{}, Sessions: []remoteproto.Session{}}
	for _, e := range s.Remote {
		out.Remote = append(out.Remote, remoteproto.RemoteExecution(e))
	}
	for _, c := range s.Certificates {
		out.Certificates = append(out.Certificates, remoteproto.Certificate{Serial: c.Serial, Key: c.Key, BenchID: c.BenchID, LeaseID: c.LeaseID, Generation: c.Generation, ExecutionID: c.ExecutionID, Holder: c.Holder, Fingerprint: c.Fingerprint, IssuedAt: stamp(c.IssuedAt), ValidBefore: stamp(c.ValidBefore)})
	}
	for _, x := range s.Sessions {
		out.Sessions = append(out.Sessions, remoteproto.Session{ID: x.ID, Serial: x.Serial, Key: x.Key, BenchID: x.BenchID, Fingerprint: x.Fingerprint, Source: x.Source, Kind: x.Kind, CommandName: x.CommandName, StartedAt: stamp(x.StartedAt), EndedAt: stamp(x.EndedAt), BytesIn: x.BytesIn, BytesOut: x.BytesOut, EndReason: x.EndReason})
	}
	reply(w, op, 200, out, "", "")
}

// stamp is RFC 3339 UTC, or empty for the zero time.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func reply(w http.ResponseWriter, operation string, status int, result any, code, message string) {
	response := remoteproto.Response{Version: remoteproto.Version, Operation: operation, OK: status == 200}
	if response.OK {
		raw, err := json.Marshal(result)
		if err != nil {
			status, response.OK, code, message = 503, false, "unavailable", "reply not encoded"
		}
		response.Result = raw
	}
	if !response.OK {
		response.Result, response.Error = nil, &remoteproto.APIError{Code: code, Message: message}
	}
	body, _ := json.Marshal(response)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

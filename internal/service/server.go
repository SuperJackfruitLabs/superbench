package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/autoarchive"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/maintenance"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocol"
)

type Config struct {
	Socket             string
	Store              bench.Store
	Operators, Readers map[uint32]bool
	MaxActive          int
	RetentionInterval  time.Duration
	ArchiveInterval    time.Duration
}
type Server struct {
	config    Config
	ctx       context.Context
	receipts  *receipts
	slots     chan struct{}
	handlers  sync.WaitGroup
	admission sync.Mutex
	closing   bool
}

func Serve(ctx context.Context, c Config) error {
	if c.MaxActive == 0 {
		c.MaxActive = 8
	}
	if c.MaxActive < 1 || c.MaxActive > 64 {
		return errors.New("invalid active operation limit")
	}
	if c.RetentionInterval == 0 {
		c.RetentionInterval = time.Minute
	}
	if c.RetentionInterval < time.Second || c.RetentionInterval > 24*time.Hour {
		return errors.New("invalid retention interval")
	}
	if c.ArchiveInterval == 0 {
		c.ArchiveInterval = time.Minute
	}
	if c.ArchiveInterval < time.Second || c.ArchiveInterval > 24*time.Hour {
		return errors.New("invalid archive interval")
	}
	listener, lock, e := listen(c.Socket, c.Store.State)
	if e != nil {
		return e
	}
	defer listener.Close()
	defer lock.Close()
	if e = c.Store.Migrate(); e != nil {
		return e
	}
	r, e := openReceipts(c.Store.State)
	if e != nil {
		return e
	}
	defer r.Close()
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &Server{config: c, ctx: lifetime, receipts: r, slots: make(chan struct{}, c.MaxActive)}
	maintenanceDone := make(chan struct{})
	go func() {
		defer close(maintenanceDone)
		maintenance.Manager{Root: c.Store.State, Store: c.Store}.Run(lifetime, c.RetentionInterval, func(e error) { log.Printf("retention blocked: %v", e) })
	}()
	archiveDone := make(chan struct{})
	go func() {
		defer close(archiveDone)
		manager := autoarchive.Manager{Store: c.Store}
		manager.Run(lifetime, c.ArchiveInterval, func(r autoarchive.Report, err error) {
			if err != nil {
				log.Printf("automatic archive blocked: %v", err)
			}
			for _, result := range r.Results {
				if result.Error != "" {
					log.Printf("automatic archive %s blocked: %s", result.Key, result.Error)
				}
			}
		})
	}()
	httpServer := &http.Server{Handler: s, ConnContext: connContext, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	shutdownDone := make(chan error, 1)
	go func() {
		<-lifetime.Done()
		s.admission.Lock()
		s.closing = true
		s.admission.Unlock()
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		shutdownDone <- httpServer.Shutdown(shutdown)
	}()
	e = httpServer.Serve(listener)
	cancel()
	shutdownErr := <-shutdownDone
	// A slow Git operation can outlive the HTTP shutdown grace period. Keep the
	// database and singleton lock until every admitted handler has reconciled.
	if shutdownErr != nil {
		_ = httpServer.Close()
	}
	s.handlers.Wait()
	<-maintenanceDone
	<-archiveDone
	if e == http.ErrServerClosed {
		return shutdownErr
	}
	return e
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v2/") {
		s.serveV2(w, r)
		return
	}
	s.admission.Lock()
	if s.closing {
		s.admission.Unlock()
		s.reply(w, protocol.Request{}, nil, 503, "unavailable", "service is shutting down")
		return
	}
	s.handlers.Add(1)
	s.admission.Unlock()
	defer s.handlers.Done()
	uid, ok := r.Context().Value(uidKey{}).(uint32)
	if !ok || (!s.config.Operators[uid] && !s.config.Readers[uid]) {
		s.reply(w, protocol.Request{}, nil, 403, "forbidden", "Unix peer is not authorized")
		return
	}
	if r.Method == "GET" && r.URL.Path == "/healthz" {
		s.reply(w, protocol.Request{}, map[string]any{"service": "superbench", "version": protocol.Version}, 200, "", "")
		return
	}
	if r.URL.Path != "/v1/operations" {
		s.reply(w, protocol.Request{}, nil, 404, "not_found", "unknown endpoint")
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		s.reply(w, protocol.Request{}, nil, 405, "invalid_request", "POST required")
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		s.reply(w, protocol.Request{}, nil, 415, "invalid_request", "JSON required")
		return
	}
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, protocol.MaxBody))
	if e != nil {
		s.reply(w, protocol.Request{}, nil, 413, "invalid_request", "request body exceeds limit")
		return
	}
	request, e := protocol.Parse(body)
	if e != nil {
		s.reply(w, protocol.Request{}, nil, 400, "invalid_request", "invalid operation request")
		return
	}
	if !protocol.ReadOnly(request.Operation) && !s.config.Operators[uid] {
		s.reply(w, request, nil, 403, "forbidden", "operator authority required")
		return
	}
	if request.Operation != "host_status" {
		if e := s.config.Store.RequireLegacy(request.Key); e != nil {
			status, code, message := operationError(e)
			s.reply(w, request, nil, status, code, message)
			return
		}
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		s.reply(w, request, nil, 503, "unavailable", "active operation limit reached")
		return
	}
	if protocol.Growth(request.Operation) {
		digest, e := request.Digest()
		if e != nil {
			s.reply(w, request, nil, 400, "invalid_request", "invalid operation request")
			return
		}
		var cached *receipt
		e = s.config.Store.WithLegacy(request.Key, func() error {
			var claimErr error
			cached, claimErr = s.receipts.Claim(uid, request.RequestID, digest)
			return claimErr
		})
		if e != nil {
			if errors.Is(e, bench.ErrWorkspace) {
				status, code, message := operationError(e)
				s.reply(w, request, nil, status, code, message)
				return
			}
			status, code := 503, "unavailable"
			message := "receipt storage unavailable; retry the same request id"
			switch {
			case errors.Is(e, errConflict):
				status, code = 409, "conflict"
				message = "request id was used with another body"
			case errors.Is(e, errInProgress):
				status, code = 409, "in_progress"
				message = "operation is still running"
			case errors.Is(e, errInDoubt):
				status, code = 409, "operation_in_doubt"
				message = "inspect bench state before retrying with another request id"
			case errors.Is(e, errCapacity):
				status = 503
				code = "unavailable"
				message = "receipt retention budget exhausted"
			}
			s.reply(w, request, nil, status, code, message)
			return
		}
		if cached != nil {
			s.raw(w, cached.HTTPStatus(), cached.Bytes())
			return
		}
	}
	result, operationErr := s.dispatch(request)
	status, code, message := 200, "", ""
	if operationErr != nil {
		status, code, message = operationError(operationErr)
	}
	response := makeResponse(request, result, code, message)
	encoded, e := json.Marshal(response)
	if e != nil || len(encoded) > protocol.MaxResponse {
		s.reply(w, request, nil, 503, "unavailable", "response exceeds transport limit; use a specific bench key")
		return
	}
	if protocol.Growth(request.Operation) {
		if e = s.receipts.Finish(uid, request.RequestID, encoded, status); e != nil {
			s.reply(w, request, result, 503, "operation_in_doubt", "receipt save failed; inspect retained bench state")
			return
		}
	}
	s.raw(w, status, encoded)
}
func (s *Server) dispatch(r protocol.Request) (any, error) {
	store := s.config.Store
	switch r.Operation {
	case "status":
		states, e := store.Status(r.Key)
		if e != nil {
			return nil, e
		}
		items := make([]*benchDTO, 0, len(states))
		for i := range states {
			items = append(items, benchView(&states[i], false))
		}
		return map[string]any{"benches": items}, nil
	case "host_status":
		h, e := store.HostStatus()
		return map[string]any{"managed": h.Managed, "data_free_bytes": h.Data.Free, "state_free_bytes": h.State.Free}, e
	case "create", "recover":
		var b *bench.Bench
		var e error
		options := bench.Options{Repo: r.Repository, Branch: r.Branch, Base: r.Base}
		if r.Initialize != nil && *r.Initialize {
			b, e = store.CreateContext(s.ctx, r.Key, options, r.Operation == "recover")
		} else {
			b, e = store.CreateColdContext(s.ctx, r.Key, options, r.Operation == "recover")
		}
		return map[string]any{"bench": benchView(b, e != nil)}, e
	case "lease":
		b, e := store.LeaseContext(s.ctx, r.Key, r.Holder)
		return map[string]any{"bench": benchView(b, true)}, e
	case "checkpoint":
		b, e := store.Checkpoint(r.Key, r.LeaseToken)
		return map[string]any{"bench": benchView(b, false)}, e
	case "release":
		e := store.Release(r.Key, r.LeaseToken)
		if e != nil {
			return nil, e
		}
		return map[string]any{"released": r.Key}, nil
	case "stop", "stop_admin":
		b, e := store.Stop(r.Key, r.LeaseToken, r.Operation == "stop_admin")
		return map[string]any{"bench": benchView(b, true)}, e
	case "run":
		checkpoint := true
		if r.Checkpoint != nil {
			checkpoint = *r.Checkpoint
		}
		result, e := store.Run(s.ctx, r.Key, r.Holder, r.Arguments, checkpoint)
		return runView(result), e
	case "setup":
		offline := false
		if r.Offline != nil {
			offline = *r.Offline
		}
		result, e := store.Setup(s.ctx, r.Key, r.Holder, offline)
		return runView(result), e
	}
	return nil, errors.New("unknown operation")
}
func operationError(e error) (int, string, string) {
	switch {
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

func makeResponse(r protocol.Request, result any, code, message string) protocol.Response {
	op := r.Operation
	if !protocol.Known(op) {
		op = ""
	}
	response := protocol.Response{Version: protocol.Version, Operation: op, RequestID: r.RequestID, OK: code == "", Result: result}
	if code != "" {
		response.Error = &protocol.APIError{Code: code, Message: message}
	}
	return response
}
func (s *Server) reply(w http.ResponseWriter, r protocol.Request, result any, status int, code, message string) {
	encoded, e := json.Marshal(makeResponse(r, result, code, message))
	if e != nil {
		http.Error(w, "serialization failed", 500)
		return
	}
	s.raw(w, status, encoded)
}
func (s *Server) raw(w http.ResponseWriter, status int, body []byte) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

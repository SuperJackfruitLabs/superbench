package client

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

func unixService(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sb-client-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = os.RemoveAll(dir) })
	return socket
}

func responseFixture(t *testing.T, name string) (protocolv2.Request, []byte, int) {
	t.Helper()
	raw, err := os.ReadFile("../protocolv2/testdata/workspace-responses.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Valid []struct {
			Name     string
			Status   int
			Expected protocolv2.Request
			Body     json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, item := range corpus.Valid {
		if item.Name != name {
			continue
		}
		r := item.Expected
		r.Version = protocolv2.Version
		if r.Operation == "run" || r.Operation == "setup" {
			r.Holder = "run:one"
		}
		if r.Operation == "run" {
			r.Arguments = []string{"/bin/false"}
		}
		if r.Operation == "create" {
			yes := true
			r.Initialize = &yes
			r.CheckpointRepository = "a"
			r.Repositories = []protocolv2.Repository{{Name: "a", Repository: "/fixture/a.git"}, {Name: "b", Repository: "/fixture/b.git"}}
		}
		return r, item.Body, item.Status
	}
	t.Fatal("missing response fixture", name)
	return protocolv2.Request{}, nil, 0
}

func TestV2UnixClientRetainsValidatedFailureGrantAndCommandExit(t *testing.T) {
	for _, name := range []string{"create-partial-grant", "run-held-grant"} {
		t.Run(name, func(t *testing.T) {
			request, body, status := responseFixture(t, name)
			var calls atomic.Int32
			socket := unixService(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/v2/operations" {
					t.Error("wrong method or fallback endpoint")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write(body)
			})
			response, err := CallV2(context.Background(), socket, request)
			var failed *WorkspaceError
			if !errors.As(err, &failed) || failed.ExitCode() == 0 || response.Result == nil || calls.Load() != 1 {
				t.Fatal("failure lost validated result or retried", err)
			}
			if request.Operation == "run" {
				out := response.Result.(*protocolv2.RunOutcome)
				if out.Grant == nil || failed.ExitCode() != 7 {
					t.Fatal("command exit or grant lost")
				}
			} else if response.Result.(*protocolv2.Mutation).Grant == nil || failed.ExitCode() != 1 {
				t.Fatal("initialization grant lost or fabricated command exit")
			}
		})
	}
}

func TestV2UnixClientRejectsAmbiguousInconsistentOrOversizedReplies(t *testing.T) {
	request := protocolv2.Request{Version: protocolv2.Version, Operation: "status"}
	valid := `{"version":"superbench.v2","operation":"status","ok":true,"result":{"benches":[]}}`
	cases := map[string]struct {
		body   string
		status int
		media  string
	}{
		"wrong operation": {strings.Replace(valid, `"operation":"status"`, `"operation":"host_status"`, 1), 200, "application/json"},
		"wrong version":   {strings.Replace(valid, "superbench.v2", "superbench.v1", 1), 200, "application/json"},
		"duplicate":       {strings.Replace(valid, `"ok":true`, `"ok":true,"ok":true`, 1), 200, "application/json"},
		"unknown":         {strings.Replace(valid, `"ok":true`, `"ok":true,"private_token":"secret"`, 1), 200, "application/json"},
		"trailing":        {valid + `{}`, 200, "application/json"},
		"null result":     {strings.Replace(valid, `{"benches":[]}`, `null`, 1), 200, "application/json"},
		"status mismatch": {valid, 503, "application/json"},
		"wrong media":     {valid, 200, "text/plain"},
		"oversized":       {strings.Repeat("x", protocolv2.MaxResponse+1), 200, "application/json"},
		"redirect":        {valid, 307, "application/json"},
	}
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			socket := unixService(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", item.media)
				w.Header().Set("Location", "http://unreachable.invalid/")
				w.WriteHeader(item.status)
				_, _ = w.Write([]byte(item.body))
			})
			response, err := CallV2(context.Background(), socket, request)
			if err == nil || response.Result != nil || calls.Load() != 1 {
				t.Fatal("invalid reply escaped or retried", err)
			}
		})
	}
}

func TestV2GrowthResponseBoundIdentityAndTransportCancellation(t *testing.T) {
	request, body, status := responseFixture(t, "run-held-grant")
	for _, item := range []struct {
		name string
		body []byte
	}{
		{"request id", []byte(strings.Replace(string(body), `"run-1"`, `"foreign-id"`, 1))},
		{"growth bound", append(body, []byte(strings.Repeat(" ", protocolv2.MaxBody))...)},
	} {
		t.Run(item.name, func(t *testing.T) {
			socket := unixService(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write(item.body)
			})
			response, err := CallV2(context.Background(), socket, request)
			if err == nil || response.Result != nil {
				t.Fatal("mismatched or oversized growth result accepted", err)
			}
		})
	}
	started := make(chan struct{})
	socket := unixService(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := CallV2(ctx, socket, request)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("transport ignored caller deadline", err)
	}
	<-started
	failed := &WorkspaceError{Code: "conflict", commandExit: 0}
	if failed.ExitCode() != 1 {
		t.Fatal("noncommand error became success")
	}
}

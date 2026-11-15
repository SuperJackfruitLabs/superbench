package workspacecompat

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/service"
)

func TestV1RefusesAggregateBeforeReceiptReservation(t *testing.T) {
	store, group := fixture(t)
	socket := filepath.Join(filepath.Dir(store.State), "api.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var serveErr error
	uid := uint32(os.Geteuid())
	go func() {
		serveErr = service.Serve(ctx, service.Config{Socket: socket, Store: store, Operators: map[uint32]bool{uid: true}, RetentionInterval: time.Hour, ArchiveInterval: time.Hour})
		close(done)
	}()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		cancel()
		select {
		case <-done:
			if serveErr != nil {
				t.Error(serveErr)
			}
		case <-time.After(15 * time.Second):
			t.Error("service failed to stop")
		}
	})
	ready := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatalf("service stopped: %v", serveErr)
		default:
		}
		response, err := httpClient.Get("http://local/healthz")
		if err == nil {
			response.Body.Close()
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("service did not start")
	}
	db, err := sql.Open("sqlite", filepath.Join(store.State, "service.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	count := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow("SELECT count(*) FROM receipts").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 0 {
		t.Fatal("unexpected initial receipts")
	}
	before := tree(t, filepath.Dir(store.State))
	requests := []protocol.Request{
		{Operation: "status"},
		{Operation: "status", Key: group.Key},
		{Operation: "create", Key: group.Key, Repository: group.Workspace.Members[0].Repository, RequestID: "create-v1"},
		{Operation: "recover", Key: group.Key, Repository: group.Workspace.Members[0].Repository, Branch: "bench/group", RequestID: "recover-v1"},
		{Operation: "lease", Key: group.Key, Holder: "writer", RequestID: "lease-v1"},
		{Operation: "run", Key: group.Key, Holder: "writer", Arguments: []string{"true"}, RequestID: "run-v1"},
		{Operation: "setup", Key: group.Key, Holder: "writer", RequestID: "setup-v1"},
		{Operation: "checkpoint", Key: group.Key, LeaseToken: group.Lease.Token},
		{Operation: "release", Key: group.Key, LeaseToken: group.Lease.Token},
		{Operation: "stop", Key: group.Key, LeaseToken: group.Lease.Token},
		{Operation: "stop_admin", Key: group.Key},
	}
	call := func(request protocol.Request) (int, protocol.Response) {
		t.Helper()
		request.Version = protocol.Version
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		response, err := httpClient.Post("http://local/v1/operations", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err = io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var body protocol.Response
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("invalid response: %v: %s", err, raw)
		}
		return response.StatusCode, body
	}
	for _, request := range requests {
		t.Run(request.Operation+"/"+request.Key, func(t *testing.T) {
			status, body := call(request)
			if status != http.StatusConflict || body.OK || body.Result != nil || body.Error == nil || body.Error.Code != "conflict" {
				t.Fatalf("misleading aggregate response: %d %+v", status, body)
			}
			if count() != 0 {
				t.Fatal("aggregate rejection reserved a receipt")
			}
			unchanged(t, store, before)
		})
	}
	for _, request := range []protocol.Request{{Operation: "status", Key: "legacy"}, {Operation: "host_status"}} {
		status, body := call(request)
		if status != http.StatusOK || !body.OK {
			t.Fatalf("valid scalar/host view refused: %d %+v", status, body)
		}
	}
	if count() != 0 {
		t.Fatal("read operations reserved receipts")
	}
	// The rejected workspace request did not consume its replay-protection ID:
	// a legitimate scalar operation with that ID still executes normally.
	status, body := call(protocol.Request{Operation: "lease", Key: "legacy", Holder: "writer", RequestID: "lease-v1"})
	if status != http.StatusOK || !body.OK || count() != 1 {
		t.Fatalf("workspace refusal poisoned a receipt ID or blocked scalar acquisition: %d %+v", status, body)
	}
}

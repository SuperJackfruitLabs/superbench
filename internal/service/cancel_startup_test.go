package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func TestPrivateStartupRefusesHistoricalStateBeforeSocketOrUpgrade(t *testing.T) {
	c, _ := serviceFixture(t, true, false)
	if err := c.Store.Migrate(); err != nil {
		t.Fatal(err)
	}
	db, err := statestore.Open(filepath.Join(c.Store.State, "bench.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=25"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err = serve(ctx, c, true); err == nil || err.Error() != fmt.Sprintf("unsupported state schema 25 (expected %d)", benchstate.SchemaVersion) {
		t.Fatal("private startup did not refuse the historical schema", err)
	}
	db, err = statestore.Open(filepath.Join(c.Store.State, "bench.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 25 {
		t.Fatal("historical schema mutated", version, err)
	}
	if _, err = os.Lstat(c.Socket); !os.IsNotExist(err) {
		t.Fatal("socket created before current-state check", err)
	}
}

func TestPrivateStartupReconcilesDurableTombstoneAndJoinsWorker(t *testing.T) {
	c, _ := serviceFixture(t, true, false)
	if err := c.Store.Migrate(); err != nil {
		t.Fatal(err)
	}
	request := protocolv2.CancelRequest{Version: protocolv2.Version, Operation: "cancel", TargetRequestID: "restart-tombstone", TargetDigest: strings.Repeat("a", 64)}
	receipts, err := openReceipts(c.Store.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = receipts.RequestCancel(uint32(os.Geteuid()), request.TargetRequestID, request.TargetDigest); err != nil {
		t.Fatal(err)
	}
	receipts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, c, true) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("private worker/service not joined")
		}
	}()
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	raw, _ := json.Marshal(request)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		response, err := client.Post("http://local/v2/operations", "application/json", bytes.NewReader(raw))
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			parsed, err := protocolv2.ParseCancelResponse(body, response.StatusCode, request)
			if err != nil {
				t.Fatal("invalid private cancel response", response.StatusCode, string(body), err)
			}
			if parsed.Result != nil && parsed.Result.State == "cancelled" {
				return
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("durable no-effect tombstone never reconciled")
		case <-tick.C:
		}
	}
}

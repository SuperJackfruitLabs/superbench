package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

// This is admission evidence, not a managed writer-fencing claim. All 64 normal
// requests are fully parsed/authenticated and held behind a real owned host lock.
func TestPrivateUnixCancellationACKWith64BlockedNormalRequests(t *testing.T) {
	s, _, _ := v2Fixture(t)
	uid := uint32(os.Geteuid())
	s.config.Operators[uid] = true
	s.proposedCancellation = true
	s.slots = make(chan struct{}, 64)
	listener, singleton, err := listenMode(s.config.Socket, s.config.Store.State, true)
	if err != nil {
		t.Fatal(err)
	}
	defer singleton.Close()
	defer listener.Close()
	peers := listener.(peerListener)
	server := &http.Server{Handler: s, ConnContext: connContext, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	defer func() { _ = server.Close(); <-served }()
	lock, err := os.OpenFile(filepath.Join(s.config.Store.State, "lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	locked := true
	unlock := func() {
		if locked {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			locked = false
		}
	}
	defer unlock()
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", s.config.Socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	post := func(raw []byte) (int, []byte, error) {
		response, err := client.Post("http://local/v2/operations", "application/json", bytes.NewReader(raw))
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		return response.StatusCode, body, err
	}
	done := make(chan error, 64)
	for n := 1; n <= 64; n++ {
		go func() {
			_, _, err := post([]byte(`{"version":"superbench.v2","operation":"status","key":"card"}`))
			done <- err
		}()
		deadline := time.NewTimer(5 * time.Second)
		tick := time.NewTicker(time.Millisecond)
		for len(peers.normal) < n || len(s.slots) < n {
			select {
			case <-deadline.C:
				tick.Stop()
				t.Fatalf("normal request %d not admitted (%d/%d)", n, len(peers.normal), len(s.slots))
			case <-tick.C:
			}
		}
		deadline.Stop()
		tick.Stop()
	}
	request := protocolv2.CancelRequest{Version: protocolv2.Version, Operation: "cancel", TargetRequestID: "saturated", TargetDigest: strings.Repeat("a", 64)}
	raw, _ := json.Marshal(request)
	status, body, err := post(raw)
	if err != nil {
		t.Fatal("actual cancellation connection could not enter", err)
	}
	response, err := protocolv2.ParseCancelResponse(body, status, request)
	if err != nil || status != 200 || response.Result == nil || response.Result.State != "cancel_requested" {
		t.Fatal(status, string(body), err)
	}
	if len(s.slots) != 64 || len(peers.normal) != 64 {
		t.Fatal("normal ceiling changed or request finished before ACK")
	}
	unlock()
	for n := 0; n < 64; n++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("normal request remained blocked")
		}
	}
}

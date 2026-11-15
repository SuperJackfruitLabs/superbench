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
	"sync"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

// Admission evidence only: actual normal requests block on a host lock while
// two completed read connections retain their reservations until physical close.
func TestPrivateUnixReceiptReadsKeepCancelAdmissionWith64NormalAnd2HeldCloses(t *testing.T) {
	s, _, _ := v2Fixture(t)
	uid := uint32(os.Geteuid())
	s.config.Operators[uid] = true
	s.proposedCancellation = true
	s.proposedReceiptLookup = true
	s.receiptSlots = make(chan struct{}, 2)
	s.slots = make(chan struct{}, 64)
	listener, singleton, err := listenMode(s.config.Socket, s.config.Store.State, true)
	if err != nil {
		t.Fatal(err)
	}
	defer singleton.Close()
	defer listener.Close()
	peers := listener.(peerListener)
	held := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	freeReads := func() { releaseOnce.Do(func() { close(release) }) }
	defer freeReads()
	wrapped := &heldReceiptListener{Listener: listener, entered: held, release: release}
	server := &http.Server{Handler: s, ConnContext: connContext, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- server.Serve(wrapped) }()
	defer func() { freeReads(); _ = server.Close(); <-served }()
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
	postAt := func(path string, raw []byte) (int, []byte, error) {
		response, err := client.Post("http://local"+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		return response.StatusCode, body, err
	}
	post := func(raw []byte) (int, []byte, error) { return postAt("/v2/operations", raw) }
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

	lookup := protocolv2.ReceiptLookupRequest{Version: protocolv2.Version, Operation: "receipt_lookup", TargetRequestID: "absent-read", TargetDigest: strings.Repeat("b", 64)}
	lookupRaw, _ := json.Marshal(lookup)
	for n := 0; n < 2; n++ {
		status, body, err := postAt("/v2/receipts/lookup", lookupRaw)
		if err != nil {
			t.Fatal(err)
		}
		out, err := protocolv2.ParseReceiptLookupResponse(body, status, lookup)
		if err != nil || status != 200 || out.Result.State != "absent" {
			t.Fatal("dedicated read blocked by normal ceiling", status, string(body), err)
		}
		select {
		case <-held:
		case <-time.After(5 * time.Second):
			t.Fatal("read socket did not reach held Close")
		}
	}
	if len(peers.reads) != 2 || cap(peers.reads)+cap(peers.normal)+cap(peers.slots) != 74 {
		t.Fatal("incorrect live connection accounting")
	}
	if status, _, err := postAt("/v2/receipts/lookup", lookupRaw); err != nil || status != 503 {
		t.Fatal("excess read queued or stole a slot", status, err)
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
	freeReads()
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

// Wrapping only accepted connections65/66 preserves peerConn's actual UID and
// mutex/slot lifecycle while deterministically suspending their physical close.
type heldReceiptListener struct {
	net.Listener
	accepted int
	entered  chan<- struct{}
	release  <-chan struct{}
}

func (l *heldReceiptListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	l.accepted++
	if l.accepted == 65 || l.accepted == 66 {
		p := c.(*peerConn)
		p.Conn = &heldReceiptClose{Conn: p.Conn, entered: l.entered, release: l.release}
	}
	return c, nil
}

type heldReceiptClose struct {
	net.Conn
	once    sync.Once
	entered chan<- struct{}
	release <-chan struct{}
}

func (c *heldReceiptClose) Close() error {
	c.once.Do(func() { c.entered <- struct{}{} })
	<-c.release
	return c.Conn.Close()
}

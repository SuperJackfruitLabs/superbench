package remoteaccess

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/activation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteproto"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/service"
)

var _ service.Remote = (*Access)(nil)

func TestNewRefusesUnlessRemoteAccessIsOn(t *testing.T) {
	root := t.TempDir()
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	addr := netip.MustParseAddrPort("127.0.0.1:2222")
	for _, s := range []activation.Switches{{}, {FreshPayload: true}, {FreshPayload: true, RemoteFleetAddress: addr}, {RemoteAccess: true, RemoteFleetAddress: addr}, {RemoteAccess: true, FreshPayload: true}} {
		if a, err := New(store, s, nil, nil); !errors.Is(err, ErrOff) || a != nil {
			t.Fatalf("%v: New %v", s, err)
		}
	}
	a, err := New(store, activation.Switches{RemoteAccess: true, FreshPayload: true, RemoteFleetAddress: addr}, nil, nil)
	if err != nil || a == nil {
		t.Fatal(err)
	}
	// New opens nothing: no state directory, no key, no listener.
	if _, err := os.Lstat(store.State); !os.IsNotExist(err) {
		t.Fatal("New touched the state directory", err)
	}
	if c, err := net.DialTimeout("tcp", addr.String(), 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("something listens on the fleet address after New")
	}
}

func unavailable(t *testing.T, f *fixture, when string) {
	t.Helper()
	c := newClient(t)
	for _, x := range []struct {
		uid  uint32
		path string
		body any
	}{
		{operator, "/v2/remote/credentials", credentialRequest("card", f.tokens["card"], c.authorized)},
		{reader, "/v2/remote/status", remoteproto.StatusRequest{Version: remoteproto.Version, Operation: remoteproto.OperationStatus}},
	} {
		if code, body := f.route(t, x.uid, x.path, x.body); code != http.StatusServiceUnavailable || !strings.Contains(string(body), `"unavailable"`) {
			t.Fatalf("%s: %s answered %d %s", when, x.path, code, body)
		}
	}
	if code, _ := f.route(t, stranger, "/v2/remote/status", remoteproto.StatusRequest{Version: remoteproto.Version, Operation: remoteproto.OperationStatus}); code != http.StatusForbidden {
		t.Fatalf("%s: stranger got %d", when, code)
	}
}

// The authority and the keys exist only once Serve runs: the routes answer
// 503 before it and after it returns, and the keys live in <state>/remote.
func TestServeOwnsTheAuthority(t *testing.T) {
	f := newFixture(t, "card")
	dir := filepath.Join(f.ops.Store.State, "remote")
	unavailable(t, f, "before Serve")
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("key directory before Serve", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.access.Serve(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if code, _ := f.route(t, reader, "/v2/remote/status", remoteproto.StatusRequest{Version: remoteproto.Version, Operation: remoteproto.OperationStatus}); code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("status never answered while serving")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st, err := os.Lstat(dir); err != nil || st.Mode().Perm() != 0700 {
		t.Fatal("key directory", err)
	}
	for _, name := range []string{"ca_ed25519", "host_ed25519"} {
		if st, err := os.Lstat(filepath.Join(dir, name)); err != nil || st.Mode().Perm() != 0600 {
			t.Fatal(name, err)
		}
	}
	if err := f.access.Serve(ctx); !errors.Is(err, ErrServing) {
		t.Fatal("second Serve", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
	unavailable(t, f, "after Serve")
}

// An activated daemon never migrates: Serve refuses state an earlier start
// did not bring current, before it creates a key, and closes its listener.
func TestServeRefusesUnmigratedState(t *testing.T) {
	root := t.TempDir()
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	if err := os.MkdirAll(store.State, 0700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(store, activation.Switches{RemoteAccess: true, FreshPayload: true, RemoteFleetAddress: netip.MustParseAddrPort(l.Addr().String())}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.listener = l
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Serve(ctx); err == nil || ctx.Err() != nil {
		t.Fatal("Serve accepted unmigrated state")
	}
	if _, err := os.Lstat(filepath.Join(store.State, "remote")); !os.IsNotExist(err) {
		t.Fatal("key directory created for refused state", err)
	}
	if c, err := net.DialTimeout("tcp", l.Addr().String(), 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("listener left open")
	}
}

// A fleet address already in use fails Serve, which benchd treats as fatal,
// and the routes stay unavailable.
func TestServeReturnsListenFailure(t *testing.T) {
	f := newFixture(t, "card")
	f.access.listener = nil // f's own listener holds the address
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.access.Serve(ctx); err == nil || ctx.Err() != nil {
		t.Fatal("Serve on a taken address", err)
	}
	unavailable(t, f, "after a listen failure")
}

// A recorded remote execution whose scope is gone (a benchd restart under
// the shipped unit, or a reboot) refuses its claims at the door; the next
// grant replaces it.
func TestAdmitRefusesWhenTheScopeIsGone(t *testing.T) {
	f := newFixture(t, "card")
	m, err := remotestate.ForStore(f.ops.Store, f.scopes)
	if err != nil {
		t.Fatal(err)
	}
	g, err := m.Grant(context.Background(), "card", f.tokens["card"])
	if err != nil {
		t.Fatal(err)
	}
	check := admit(m)
	if a, err := check(g.Claims); err != nil || a.Claims != g.Claims {
		t.Fatal("live scope refused", err)
	}
	f.scopes.gone(g.Claims.ExecutionID)
	if _, err := check(g.Claims); !errors.Is(err, remotestate.ErrRevoked) {
		t.Fatal("claims admitted with their scope gone", err)
	}
	if _, err := m.Admit(g.Claims); err != nil {
		t.Fatal("state alone still admits; the door check is what refuses", err)
	}
	next, err := m.Grant(context.Background(), "card", f.tokens["card"])
	if err != nil || next.Claims.ExecutionID == g.Claims.ExecutionID {
		t.Fatal("grant reused a dead scope", err)
	}
	if _, err := check(next.Claims); err != nil {
		t.Fatal(err)
	}
}

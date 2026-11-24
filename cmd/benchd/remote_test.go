package main

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/activation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteaccess"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/service"
)

// countNew replaces newRemote for one test and counts its calls.
func countNew(t *testing.T) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	real := newRemote
	newRemote = func(store bench.Store, s activation.Switches, operators, readers map[uint32]bool) (*remoteaccess.Access, error) {
		calls.Add(1)
		return real(store, s, operators, readers)
	}
	t.Cleanup(func() { newRemote = real })
	return &calls
}

// benchd builds remote access only when activation enables it, and hands
// the server exactly that adapter.
func TestRunBuildsRemoteAccessOnlyWhenOn(t *testing.T) {
	calls := countNew(t)
	c := daemonConfig(t, activation.Switches{})
	var got *service.Config
	serve := func(_ context.Context, config service.Config) error { got = &config; return nil }
	off := activation.Switches{FreshPayload: true, Cancellation: true}
	if err := run(daemonArgs(c), func() (activation.Switches, error) { return off, nil }, serve); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Remote != nil || calls.Load() != 0 {
		t.Fatalf("off: remote %v, New called %d times", got, calls.Load())
	}
	on := off
	on.RemoteAccess, on.RemoteFleetAddress = true, netip.MustParseAddrPort("127.0.0.1:2222")
	if err := run(daemonArgs(c), func() (activation.Switches, error) { return on, nil }, serve); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Remote.(*remoteaccess.Access); !ok || calls.Load() != 1 || got.Activation != on {
		t.Fatalf("on: remote %T, New called %d times", got.Remote, calls.Load())
	}
	if _, err := os.Lstat(filepath.Join(c.Store.State, "remote")); !os.IsNotExist(err) {
		t.Fatal("building remote access opened its key directory", err)
	}
}

// remote_access without fresh_payload refuses before any socket.
func TestBenchdRefusesRemoteWithoutFresh(t *testing.T) {
	c := daemonConfig(t, activation.Switches{})
	if err := c.Store.Migrate(); err != nil {
		t.Fatal(err)
	}
	load := func() (activation.Switches, error) {
		return activation.Switches{RemoteAccess: true, Cancellation: true, RemoteFleetAddress: netip.MustParseAddrPort("127.0.0.1:2222")}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := run(daemonArgs(c), load, func(_ context.Context, config service.Config) error { return service.Serve(ctx, config) })
	if err == nil || !strings.Contains(err.Error(), "remote_access requires fresh_payload") {
		t.Fatal("start not refused", err)
	}
	for _, p := range []string{c.Socket, filepath.Join(c.Store.State, "remote")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatal(p, "created before the refusal", err)
		}
	}
}

type fakeRemote struct {
	serve func(context.Context) error
}

func (fakeRemote) Route(w http.ResponseWriter, _ *http.Request, _ uint32, _ func(*http.Request) error) {
	w.WriteHeader(http.StatusTeapot)
}
func (f fakeRemote) Serve(ctx context.Context) error { return f.serve(ctx) }

// A remote failure stops benchd and is its error; a clean shutdown waits
// for the remote listener to finish before the server returns.
func TestServerRunsRemoteAccessForItsLifetime(t *testing.T) {
	c := daemonConfig(t, activation.Switches{})
	boom := errors.New("fleet address unavailable")
	c.Remote = fakeRemote{serve: func(context.Context) error { return boom }}
	done := make(chan error, 1)
	go func() { done <- service.Serve(context.Background(), c) }()
	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Fatal("remote failure", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("benchd kept running after its remote listener failed")
	}

	c = daemonConfig(t, activation.Switches{})
	var finished atomic.Bool
	started := make(chan struct{})
	c.Remote = fakeRemote{serve: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond)
		finished.Store(true)
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- service.Serve(ctx, c) }()
	<-started
	cancel()
	if err := <-done; err != nil || !finished.Load() {
		t.Fatalf("shutdown returned %v before the remote finished (%t)", err, finished.Load())
	}
}

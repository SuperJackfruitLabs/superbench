package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/activation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/service"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

func daemonConfig(t *testing.T, switches activation.Switches) service.Config {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sb-activation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	uid := uint32(os.Geteuid())
	return service.Config{Socket: filepath.Join(root, "api.sock"), Store: bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")},
		Operators: map[uint32]bool{uid: true}, Readers: map[uint32]bool{}, Activation: switches}
}

func daemonArgs(c service.Config) []string {
	return []string{"-state", c.Store.State, "-data", c.Store.Data, "-socket", c.Socket, "-operator-uids", strconv.Itoa(os.Geteuid())}
}

// refusedBeforeSocket starts the real server and expects it to refuse with why
// before it creates the socket.
func refusedBeforeSocket(t *testing.T, c service.Config, why string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	err := service.Serve(ctx, c)
	cancel()
	if err == nil || !strings.Contains(err.Error(), why) {
		t.Fatalf("%v: start was not refused with %q: %v", c.Activation, why, err)
	}
	if _, err = os.Lstat(c.Socket); !os.IsNotExist(err) {
		t.Fatalf("%v: socket created before the refusal: %v", c.Activation, err)
	}
}

// An activated daemon never migrates. It refuses state that no earlier start
// brought current, before it creates the socket.
func TestActivatedStartupRefusesUnmigratedStateBeforeSocket(t *testing.T) {
	for _, switches := range []activation.Switches{{ReceiptLookup: true}, {Cancellation: true}, {Cancellation: true, ReceiptLookup: true}} {
		refusedBeforeSocket(t, daemonConfig(t, switches), "activated paths need state migrated")
	}
}

// A combination that would strand workspaces refuses before the socket, even
// with state migrated, so nothing else would have stopped the start.
func TestStartupRefusesCombinationsThatStrandWorkspaces(t *testing.T) {
	for _, c := range []struct {
		switches activation.Switches
		why      string
	}{
		{activation.Switches{FiniteIO: true, Cancellation: true}, "finite_io requires fresh_payload"},
		{activation.Switches{RegistrationObservation: true, Cancellation: true}, "registration_observation requires fresh_payload"},
		{activation.Switches{FreshPayload: true, ReceiptLookup: true}, "fresh_payload requires cancellation"},
	} {
		config := daemonConfig(t, c.switches)
		if err := config.Store.Migrate(); err != nil {
			t.Fatal(err)
		}
		refusedBeforeSocket(t, config, c.why)
	}
}

// fresh_payload resolves the real installed identity at start. A test host has
// none for its own uid, so the start refuses before the socket.
func TestStartupRefusesFreshPayloadWithoutInstalledIdentity(t *testing.T) {
	if _, err := serviceidentity.ResolveService(); err == nil {
		t.Skip("this host has an installed service identity for the test uid")
	}
	for _, switches := range []activation.Switches{{FreshPayload: true, Cancellation: true}, {FreshPayload: true, FiniteIO: true, ReceiptLookup: true, Cancellation: true, RegistrationObservation: true}} {
		config := daemonConfig(t, switches)
		if err := config.Store.Migrate(); err != nil {
			t.Fatal(err)
		}
		refusedBeforeSocket(t, config, "fresh_payload requires the installed payload policy and service identity")
	}
}

// benchd passes exactly the loaded switches to the server, and a loader
// refusal stops the start before the server runs.
func TestRunPassesTheLoadedSwitchesToTheServer(t *testing.T) {
	c := daemonConfig(t, activation.Switches{})
	want := activation.Switches{FreshPayload: true, FiniteIO: true, Cancellation: true}
	var got *service.Config
	serve := func(_ context.Context, config service.Config) error { got = &config; return nil }
	if err := run(daemonArgs(c), func() (activation.Switches, error) { return want, nil }, serve); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Activation != want || got.Socket != c.Socket {
		t.Fatalf("server received %+v", got)
	}
	got = nil
	err := run(daemonArgs(c), func() (activation.Switches, error) { return activation.Switches{}, activation.ErrActivation }, serve)
	if !errors.Is(err, activation.ErrActivation) || !strings.Contains(err.Error(), activation.FixedPath) || got != nil {
		t.Fatalf("loader refusal did not stop the start: %v %+v", err, got)
	}
}

// Through run and the real server: receipt lookup alone answers its route and
// runs the private listener, whose accept class holds eight connections. With
// every switch off the route is 404 and a ninth connection stays open.
func TestReceiptLookupAloneReachesTheDaemonsPrivateListener(t *testing.T) {
	for _, on := range []bool{false, true} {
		c := daemonConfig(t, activation.Switches{})
		if err := c.Store.Migrate(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		load := func() (activation.Switches, error) { return activation.Switches{ReceiptLookup: on}, nil }
		go func() {
			done <- run(daemonArgs(c), load, func(_ context.Context, config service.Config) error { return service.Serve(ctx, config) })
		}()
		dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
		}
		client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: dial}}
		status := 0
		for deadline := time.Now().Add(5 * time.Second); status == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if response, err := client.Post("http://local/v2/receipts/lookup", "application/json", strings.NewReader("{}")); err == nil {
				status = response.StatusCode
				response.Body.Close()
			}
		}
		var held []net.Conn
		for range 8 {
			conn, err := net.Dial("unix", c.Socket)
			if err != nil {
				t.Fatal(err)
			}
			held = append(held, conn)
		}
		ninth, err := net.Dial("unix", c.Socket)
		if err != nil {
			t.Fatal(err)
		}
		_ = ninth.SetReadDeadline(time.Now().Add(time.Second))
		_, readErr := ninth.Read(make([]byte, 1))
		var timeout net.Error
		closed := !(errors.As(readErr, &timeout) && timeout.Timeout())
		for _, conn := range append(held, ninth) {
			conn.Close()
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if status == 0 || on == (status == http.StatusNotFound) || closed != on {
			t.Fatalf("receipt_lookup=%t: route answered %d, ninth connection closed %t (%v)", on, status, closed, readErr)
		}
	}
}

package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/activation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/service"
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

// An activated daemon never migrates. It refuses state that no earlier start
// brought current, before it creates the socket.
func TestActivatedStartupRefusesUnmigratedStateBeforeSocket(t *testing.T) {
	for _, switches := range []activation.Switches{{FiniteIO: true}, {FreshPayload: true}, {RegistrationObservation: true}, {Cancellation: true, ReceiptLookup: true}} {
		c := daemonConfig(t, switches)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		err := service.Serve(ctx, c)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "activated paths need state migrated") {
			t.Fatalf("%v: activated start accepted unmigrated state: %v", switches, err)
		}
		if _, err = os.Lstat(c.Socket); !os.IsNotExist(err) {
			t.Fatalf("%v: socket created before the state check: %v", switches, err)
		}
	}
}

// The registration observation route answers only when the activation switch
// is on; with the zero value the daemon answers exactly as before (404).
func TestActivationSwitchReachesTheDaemon(t *testing.T) {
	for _, on := range []bool{false, true} {
		c := daemonConfig(t, activation.Switches{RegistrationObservation: on})
		if err := c.Store.Migrate(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- service.Serve(ctx, c) }()
		client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
		}}}
		status := 0
		for deadline := time.Now().Add(5 * time.Second); status == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if response, err := client.Post("http://local/v2/registrations/observe", "application/json", strings.NewReader("{}")); err == nil {
				status = response.StatusCode
				response.Body.Close()
			}
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if on == (status == http.StatusNotFound) || status == 0 {
			t.Fatalf("registration observation %t answered %d", on, status)
		}
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
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

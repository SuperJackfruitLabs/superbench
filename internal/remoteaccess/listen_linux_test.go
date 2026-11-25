package remoteaccess

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteproto"
)

// absent4 and absent6 are documentation addresses (RFC 5737, RFC 3849): no
// host carries them, so they stand in for a tailnet address not yet up.
var (
	absent4 = netip.MustParseAddr("192.0.2.77")
	absent6 = netip.MustParseAddr("2001:db8::77")
)

func requireNonlocalBindOff(t *testing.T) {
	t.Helper()
	// A host that allows every non-local bind would pass without freebind.
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_nonlocal_bind"); err == nil && strings.TrimSpace(string(b)) != "0" {
		t.Fatalf("net.ipv4.ip_nonlocal_bind is %q; this test needs it off", strings.TrimSpace(string(b)))
	}
}

// The fleet address may not exist yet when benchd starts: the bind succeeds,
// on that exact address.
func TestFleetListenerBindsBeforeTheAddressExists(t *testing.T) {
	requireNonlocalBindOff(t)
	l, err := listenFleet(context.Background(), netip.AddrPortFrom(absent4, 0))
	if err != nil {
		t.Fatal("bind before the address exists:", err)
	}
	defer l.Close()
	if got := l.Addr().(*net.TCPAddr).AddrPort().Addr(); got != absent4 {
		t.Fatalf("bound %s, not the configured address", got)
	}
	// An address in use is still a bind error.
	if again, err := listenFleet(context.Background(), l.Addr().(*net.TCPAddr).AddrPort()); err == nil {
		again.Close()
		t.Fatal("a second bind of the same address succeeded")
	}
}

// Serve itself starts on an address the host does not carry yet, and opens
// the routes, instead of failing the start.
func TestServeStartsBeforeTheAddressExists(t *testing.T) {
	requireNonlocalBindOff(t)
	f := newFixture(t, "card")
	f.access.listener = nil
	probe, err := listenFleet(context.Background(), netip.AddrPortFrom(absent4, 0))
	if err != nil {
		t.Fatal(err)
	}
	f.access.address = probe.Addr().(*net.TCPAddr).AddrPort()
	probe.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.access.Serve(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, _ := f.route(t, reader, "/v2/remote/status", remoteproto.StatusRequest{Version: remoteproto.Version, Operation: remoteproto.OperationStatus})
		if code == http.StatusOK {
			break
		}
		select {
		case err := <-done:
			cancel()
			t.Fatal("Serve failed on an address not yet up:", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("routes never opened")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

const arrivesChild = "SUPERBENCH_FREEBIND_NETNS_CHILD"

// Once the address arrives, the listener bound before it serves it. The
// test re-runs itself in a fresh network namespace (a user namespace too
// when not root), where it can add the address to loopback. Without the
// namespace rights it skips unless it runs as root, where it must pass: the
// privileged CI lane runs it as root (scripts/check-identity-ci.py).
func TestFleetListenerServesOnceTheAddressArrives(t *testing.T) {
	if os.Getenv(arrivesChild) == "1" {
		arrives(t)
		return
	}
	if _, err := exec.LookPath("ip"); err != nil {
		if os.Geteuid() == 0 {
			t.Fatal("ip is required as root:", err)
		}
		t.Skip("ip not installed")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFleetListenerServesOnceTheAddressArrives$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), arrivesChild+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
	if os.Geteuid() != 0 {
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}}
	}
	out, err := cmd.CombinedOutput()
	t.Logf("namespace child:\n%s", out)
	var exit *exec.ExitError
	switch {
	case err == nil:
	case os.Geteuid() != 0 && (errors.As(err, &exit) && exit.ExitCode() == noNamespaceRights || !errors.As(err, &exit)):
		t.Skip("no network namespace rights here; the root run proves this:", err)
	default:
		t.Fatal(err)
	}
}

const noNamespaceRights = 77

func arrives(t *testing.T) {
	if out, err := exec.Command("ip", "link", "set", "lo", "up").CombinedOutput(); err != nil {
		t.Logf("ip link set lo up: %v %s", err, out)
		os.Exit(noNamespaceRights)
	}
	requireNonlocalBindOff(t)
	for _, x := range []struct {
		addr   netip.Addr
		add    []string
		marker string // the privileged CI lane requires each one
	}{
		{absent4, []string{"addr", "add", "192.0.2.77/32", "dev", "lo"}, "FREEBIND_IPV4_SERVED_ONCE_THE_ADDRESS_ARRIVED"},
		{absent6, []string{"-6", "addr", "add", "2001:db8::77/128", "dev", "lo", "nodad"}, "FREEBIND_IPV6_SERVED_ONCE_THE_ADDRESS_ARRIVED"},
	} {
		l, err := listenFleet(context.Background(), netip.AddrPortFrom(x.addr, 0))
		if err != nil {
			t.Fatalf("%s: bind before the address exists: %v", x.addr, err)
		}
		at := l.Addr().String()
		if c, err := net.DialTimeout("tcp", at, 300*time.Millisecond); err == nil {
			c.Close()
			t.Fatalf("%s: connected before the address exists", at)
		}
		if out, err := exec.Command("ip", x.add...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v %s", x.add, err, out)
		}
		accepted := make(chan string, 1)
		go func() {
			c, err := l.Accept()
			if err != nil {
				accepted <- err.Error()
				return
			}
			defer c.Close()
			b, _ := io.ReadAll(io.LimitReader(c, 5))
			accepted <- string(b)
		}()
		c, err := net.DialTimeout("tcp", at, 5*time.Second)
		if err != nil {
			t.Fatalf("%s: no connection once the address exists: %v", at, err)
		}
		c.Write([]byte("hello"))
		c.Close()
		if got := <-accepted; got != "hello" {
			t.Fatalf("%s: accepted %q", at, got)
		}
		l.Close()
		t.Logf("%s: bound before the address existed, served once it arrived", at)
		t.Log(x.marker)
	}
}

package remoteaccess

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/activation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
)

// hostFixture is a host meeting every requirement, under one temporary
// directory: its own /var/tmp, /dev/shm and shared scratch, and roots
// outside them.
func hostFixture(t *testing.T) (host, bench.Store) {
	t.Helper()
	base := t.TempDir()
	h := host{varTmp: filepath.Join(base, "var-tmp"), devShm: filepath.Join(base, "dev-shm"), mountSetattr: func() error { return nil }}
	h.shared = []string{filepath.Join(base, "tmp"), h.varTmp, h.devShm}
	for _, d := range h.shared {
		if err := os.Mkdir(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	return h, bench.Store{State: filepath.Join(base, "srv", "state"), Data: filepath.Join(base, "srv", "data")}
}

func refusedFor(t *testing.T, err error, want string) {
	t.Helper()
	if !errors.Is(err, ErrHost) || !strings.Contains(err.Error(), want) {
		t.Fatalf("want a refusal naming %q, got %v", want, err)
	}
}

func TestHostMeetingEveryRequirementIsAccepted(t *testing.T) {
	h, store := hostFixture(t)
	if err := h.check(store); err != nil {
		t.Fatal(err)
	}
}

func TestHostWithoutMountSetattrIsRefused(t *testing.T) {
	h, store := hostFixture(t)
	h.mountSetattr = func() error { return syscall.ENOSYS }
	refusedFor(t, h.check(store), "mount_setattr")
}

func TestHostWithSymlinkedVarTmpIsRefused(t *testing.T) {
	h, store := hostFixture(t)
	real := h.varTmp + "-real"
	if err := os.Rename(h.varTmp, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, h.varTmp); err != nil {
		t.Fatal(err)
	}
	refusedFor(t, h.check(store), h.varTmp+" must be a directory, not a symlink")
	if err := os.Remove(h.varTmp); err != nil {
		t.Fatal(err)
	}
	refusedFor(t, h.check(store), h.varTmp) // missing entirely
}

func TestHostWithoutDevShmIsRefused(t *testing.T) {
	h, store := hostFixture(t)
	if err := os.Remove(h.devShm); err != nil {
		t.Fatal(err)
	}
	refusedFor(t, h.check(store), h.devShm+" must exist")
}

func TestRootsUnderSharedScratchAreRefused(t *testing.T) {
	h, store := hostFixture(t)
	for _, shared := range h.shared {
		for _, under := range []string{shared, filepath.Join(shared, "superbench", "x")} {
			s := store
			s.Data = under
			refusedFor(t, h.check(s), "data root "+under+" must not be under "+shared)
			s = store
			s.State = under
			refusedFor(t, h.check(s), "state root "+under+" must not be under "+shared)
		}
	}
	// A sibling that only shares a prefix is not under it.
	s := store
	s.Data = h.shared[0] + "-data"
	if err := h.check(s); err != nil {
		t.Fatal("prefix sibling refused:", err)
	}
}

// Serve refuses a host missing a requirement before it opens the key
// directory, and closes its listener, so benchd does not start.
func TestServeRefusesAHostMissingARequirement(t *testing.T) {
	h, store := hostFixture(t)
	h.mountSetattr = func() error { return syscall.ENOSYS }
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(store, activation.Switches{RemoteAccess: true, FreshPayload: true, RemoteFleetAddress: netip.MustParseAddrPort(l.Addr().String())}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.listener, a.host = l, &h
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	refusedFor(t, a.Serve(ctx), "mount_setattr")
	if _, err := os.Lstat(filepath.Join(store.State, "remote")); !os.IsNotExist(err) {
		t.Fatal("key directory created on a refused host", err)
	}
	if c, err := net.Dial("tcp", l.Addr().String()); err == nil {
		c.Close()
		t.Fatal("listener left open on a refused host")
	}
}

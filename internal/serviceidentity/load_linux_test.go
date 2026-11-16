//go:build linux

package serviceidentity

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestServiceCredentialsRequireExactActualTuple(t *testing.T) {
	p := registryFixture().Policy
	u, g := int(p.DaemonUID), int(p.DaemonGID)
	if err := serviceCredentials(p, u, u, g, g); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][4]int{
		{0, 0, 0, 0}, {u, 0, g, g}, {0, u, g, g},
		{int(p.PayloadUID), int(p.PayloadUID), int(p.PayloadGID), int(p.PayloadGID)},
		{u, u, g + 1, g + 1}, {u, u, g, g + 1}, {u, u + 1, g, g},
		{u + 1, u + 1, g, g}, {-1, u, g, g},
	} {
		if err := serviceCredentials(p, ids[0], ids[1], ids[2], ids[3]); !errors.Is(err, ErrAuthority) {
			t.Fatalf("unexpected credential authority for %v: %v", ids, err)
		}
	}
	if err := serviceCredentials(Registry{}.Policy, 0, 0, 0, 0); !errors.Is(err, ErrAuthority) {
		t.Fatalf("root/zero policy: %v", err)
	}
}

func TestRegistryLeafRequiresExactModeOwnerAndSingleRegularInode(t *testing.T) {
	dir := t.TempDir()
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	path := filepath.Join(dir, "identity")
	raw, err := registryFixture().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	file, first, err := openLeaf(parent, "identity", uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	refuse := func(name string, owner uint32) {
		t.Helper()
		file, _, err := openLeaf(parent, name, owner)
		if file != nil || !errors.Is(err, ErrAuthority) {
			t.Fatalf("accepted %s: %v %v", name, file, err)
		}
	}
	refuse("identity", uint32(os.Getuid())+1)
	for _, mode := range []os.FileMode{0600, 0640, 0664, 0644 | os.ModeSetuid, 0644 | os.ModeSetgid, 0644 | os.ModeSticky} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != mode {
			t.Fatalf("fixture did not set mode %v: %v %v", mode, info, err)
		}
		refuse("identity", uint32(os.Getuid()))
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	refuse("identity", uint32(os.Getuid()))
	refuse("linked", uint32(os.Getuid()))
	if err := os.Remove(filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(dir, "symlink")); err != nil {
		t.Fatal(err)
	}
	refuse("symlink", uint32(os.Getuid()))
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0755); err != nil {
		t.Fatal(err)
	}
	refuse("directory", uint32(os.Getuid()))
	// Recreating identical bytes cannot preserve the original physical snapshot.
	if err := os.WriteFile(filepath.Join(dir, "replacement"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "replacement"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "replacement"), path); err != nil {
		t.Fatal(err)
	}
	file, current, err := openLeaf(parent, "identity", uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if current == first || current.directoryIdentity == first.directoryIdentity {
		t.Fatal("byte-identical replacement reused physical identity")
	}
}

func TestRegistryFIFORefusesBeforeWaitingForWriter(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0644); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestRegistryFIFOProbe$", "-test.v")
	cmd.Env = append(os.Environ(), "SUPERBENCH_REGISTRY_FIFO_PARENT="+dir)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil || err != nil || !bytes.Contains(out, []byte("--- PASS: TestRegistryFIFOProbe")) {
		t.Fatalf("FIFO refusal blocked or failed: %v %v %s", ctx.Err(), err, out)
	}
}

func TestRegistryFIFOProbe(t *testing.T) {
	dir := os.Getenv("SUPERBENCH_REGISTRY_FIFO_PARENT")
	if dir == "" {
		return
	}
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	file, _, err := openLeaf(parent, "fifo", uint32(os.Getuid()))
	if file != nil || !errors.Is(err, ErrAuthority) {
		t.Fatalf("FIFO authority: %v %v", file, err)
	}
}

func TestInstalledAncestorsRejectSymlinkAndUnprotectedDirectory(t *testing.T) {
	dir := t.TempDir()
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := os.Symlink(dir, filepath.Join(dir, "redirect")); err != nil {
		t.Fatal(err)
	}
	if file, err := openDirectory(parent, "redirect"); file != nil || !errors.Is(err, ErrAuthority) {
		t.Fatalf("symlink ancestor: %v %v", file, err)
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := directoryID(parent); !errors.Is(err, ErrAuthority) {
		t.Fatalf("writable directory: %v", err)
	}
	// An ordinary sandbox may construct its own non-root-owned filesystem
	// root. Positive installed-root custody requires the privileged fixed-path
	// fixture, rather than treating that sandbox root as installation authority.
}

//go:build linux && serviceidentityfixture

package payloadexec

// Explicitly opt-in, CGO_ENABLED=0 fixture. An outer root supervisor must own
// the bounded cgroup and private mount/network namespaces; no PID namespace.
// This tests actual fixed-path loader binding, not payload execution/fencing.
import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

func helperFixtureNamespaceGuard(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUPERBENCH_IDENTITY_FIXTURE") != "1" {
		t.Fatal("explicit root identity fixture required")
	}
	pid, err := os.Readlink("/proc/self/ns/pid")
	initial, initialErr := os.Readlink("/proc/1/ns/pid")
	if err != nil || initialErr != nil || pid != initial {
		t.Fatal("fixture requires original PID namespace")
	}
	for _, kind := range []string{"mnt", "net"} {
		expected := os.Getenv("SUPERBENCH_IDENTITY_ORIGINAL_" + strings.ToUpper(kind))
		original, err := os.Readlink("/proc/1/ns/" + kind)
		current, currentErr := os.Readlink("/proc/self/ns/" + kind)
		var a, b syscall.Stat_t
		if err != nil || currentErr != nil || expected != original || !strings.HasPrefix(original, kind+":[") || !strings.HasPrefix(current, kind+":[") || current == original || syscall.Stat("/proc/1/ns/"+kind, &a) != nil || syscall.Stat("/proc/self/ns/"+kind, &b) != nil || a.Dev == b.Dev && a.Ino == b.Ino {
			t.Fatalf("private %s namespace not proven", kind)
		}
	}
}

func TestInstalledHelperPolicyRegistryBindingFixture(t *testing.T) {
	helperFixtureNamespaceGuard(t) // Before mkdir, mounts, file writes or children.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/var/tmp", "superbench-helper-binding-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	etc := filepath.Join(root, "etc")
	if err := os.Mkdir(etc, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(etc, 0755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mount(etc, "/etc", "", syscall.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := syscall.Unmount("/etc", 0); err != nil {
			t.Errorf("private /etc retained: %v", err)
			return
		}
		if t.Failed() {
			t.Logf("failed fixture preserved at %s", root)
			return
		}
		// Exact owned files/directories only. Never recursive cleanup as root.
		for _, name := range []string{"payload-policy.json", "service-identity.json"} {
			if err := os.Remove(filepath.Join(etc, "superbench", name)); err != nil {
				t.Error(err)
			}
		}
		for _, p := range []string{filepath.Join(etc, "superbench"), etc, root} {
			if err := os.Remove(p); err != nil {
				t.Error(err)
			}
		}
	}()
	if err := os.Mkdir("/etc/superbench", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod("/etc/superbench", 0755); err != nil {
		t.Fatal(err)
	}
	p := Policy{DaemonUID: 61001, DaemonGID: 61003, PayloadUID: 61002, PayloadGID: 61004, DataRoot: "/data", StateRoot: "/state", CgroupRoot: "/cgroup"}
	write := func(t *testing.T, path string, raw []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, raw, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	publish := func(t *testing.T, policy Policy) {
		t.Helper()
		raw, err := policy.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		registry, err := serviceidentity.ForPolicy(policy)
		if err != nil {
			t.Fatal(err)
		}
		public, err := registry.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		write(t, FixedPolicy, raw, 0600)
		write(t, serviceidentity.FixedRegistry, public, 0644)
	}
	reset := func(t *testing.T) helperBinding {
		t.Helper()
		for _, name := range []string{FixedPolicy, serviceidentity.FixedRegistry, "/etc/superbench/private-target", "/etc/superbench/private-link", "/etc/superbench/new-registry"} {
			if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
		publish(t, p)
		binding, err := loadHelperBinding()
		if err != nil {
			t.Fatalf("root0600 positive control: %v", err)
		}
		if err := binding.recheck(); err != nil {
			t.Fatalf("positive recheck: %v", err)
		}
		return binding
	}
	reset(t)
	t.Log("HELPER_BINDING_FIXED_PRIVATE_PUBLIC_POSITIVE")
	for _, scenario := range []string{"0644", "setuid", "hardlink", "symlink", "FIFO"} {
		if !t.Run(scenario, func(t *testing.T) {
			reset(t) // A successful same-fixture load precedes every refusal.
			switch scenario {
			case "0644":
				if err := os.Chmod(FixedPolicy, 0644); err != nil {
					t.Fatal(err)
				}
			case "setuid":
				if err := syscall.Chmod(FixedPolicy, 04600); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(FixedPolicy, "/etc/superbench/private-link"); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(FixedPolicy, "/etc/superbench/private-target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("private-target", FixedPolicy); err != nil {
					t.Fatal(err)
				}
			case "FIFO":
				if err := os.Remove(FixedPolicy); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(FixedPolicy, 0600); err != nil {
					t.Fatal(err)
				}
				binary, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, binary, "-test.run=^TestHelperBindingPrivateFIFOChild$", "-test.v")
				child.Env = []string{"PATH=/usr/bin:/bin", "SUPERBENCH_IDENTITY_FIXTURE=1", "SUPERBENCH_HELPER_FIFO_CHILD=1", "SUPERBENCH_IDENTITY_ORIGINAL_MNT=" + os.Getenv("SUPERBENCH_IDENTITY_ORIGINAL_MNT"), "SUPERBENCH_IDENTITY_ORIGINAL_NET=" + os.Getenv("SUPERBENCH_IDENTITY_ORIGINAL_NET")}
				out, err := child.CombinedOutput()
				if ctx.Err() != nil || err != nil || !bytes.Contains(out, []byte("HELPER_BINDING_FIFO_REFUSED_NO_FRAME")) {
					t.Fatalf("FIFO failed or blocked: %v %v %s", ctx.Err(), err, out)
				}
				return
			}
			binding, err := loadHelperBinding()
			if binding != (helperBinding{}) || !errors.Is(err, ErrPolicy) {
				t.Fatalf("private %s did not refuse ErrPolicy: %#v %v", scenario, binding, err)
			}
		}) {
			return
		}
	}
	t.Log("HELPER_BINDING_PRIVATE_MODE_LINK_TYPE_REFUSALS")
	if !t.Run("missing registry before frame", func(t *testing.T) {
		reset(t)
		if err := os.Remove(serviceidentity.FixedRegistry); err != nil {
			t.Fatal(err)
		}
		binding, err := loadHelperBinding()
		if binding != (helperBinding{}) || !errors.Is(err, serviceidentity.ErrAuthority) {
			t.Fatalf("missing registry: %#v %v", binding, err)
		}
	}) {
		return
	}
	if !t.Run("mismatch registry before frame", func(t *testing.T) {
		reset(t)
		different := p
		different.PayloadUID++
		registry, err := serviceidentity.ForPolicy(different)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := registry.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		write(t, serviceidentity.FixedRegistry, raw, 0644)
		binding, err := loadHelperBinding()
		if binding != (helperBinding{}) || !errors.Is(err, serviceidentity.ErrAuthority) {
			t.Fatalf("registry mismatch: %#v %v", binding, err)
		}
	}) {
		return
	}
	t.Log("HELPER_BINDING_MISSING_MISMATCHED_REGISTRY_NO_FRAME")
	if !t.Run("matching changed policies revoke old binding", func(t *testing.T) {
		old := reset(t)
		changed := p
		changed.DataRoot = "/other-data"
		publish(t, changed)
		fresh, err := loadHelperBinding()
		if err != nil || fresh.policy != changed {
			t.Fatalf("changed positive: %#v %v", fresh, err)
		}
		if err := old.recheck(); !errors.Is(err, serviceidentity.ErrAuthority) {
			t.Fatalf("old policy binding revived: %v", err)
		}
	}) {
		return
	}
	if !t.Run("byte identical new registry inode revokes old binding", func(t *testing.T) {
		old := reset(t)
		before, err := os.Stat(serviceidentity.FixedRegistry)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(serviceidentity.FixedRegistry)
		if err != nil {
			t.Fatal(err)
		}
		write(t, "/etc/superbench/new-registry", raw, 0644)
		if err := os.Rename("/etc/superbench/new-registry", serviceidentity.FixedRegistry); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(serviceidentity.FixedRegistry)
		if err != nil || os.SameFile(before, after) {
			t.Fatal("registry replacement not proven")
		}
		if _, err := loadHelperBinding(); err != nil {
			t.Fatalf("replacement positive: %v", err)
		}
		if err := old.recheck(); !errors.Is(err, serviceidentity.ErrAuthority) {
			t.Fatalf("original inode binding revived: %v", err)
		}
	}) {
		return
	}
	reset(t)
	t.Log("HELPER_BINDING_CHANGED_PROFILE_AND_IDENTICAL_REPLACEMENT_REFUSED")
}

func TestHelperBindingPrivateFIFOChild(t *testing.T) {
	if os.Getenv("SUPERBENCH_HELPER_FIFO_CHILD") != "1" {
		return
	}
	helperFixtureNamespaceGuard(t)
	binding, err := loadHelperBinding()
	if binding != (helperBinding{}) || !errors.Is(err, ErrPolicy) {
		t.Fatalf("FIFO did not refuse before frame: %#v %v", binding, err)
	}
	t.Log("HELPER_BINDING_FIFO_REFUSED_NO_FRAME")
}

//go:build linux && serviceidentityfixture

package quotaoperationcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

// This fixture proves fixed-path descriptor custody only, without storage
// initialization, XFS effects, service changes or installed lifecycle claims.
func TestInstalledIndexedPolicyDescriptorContinuity(t *testing.T) {
	if os.Getenv("SUPERBENCH_INDEXED_POLICY_FIXTURE") != "1" {
		t.Skip("requires explicit isolated root fixture")
	}
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		t.Fatal("requires root")
	}
	for _, kind := range []string{"mnt", "net"} {
		original, err := os.Stat("/proc/1/ns/" + kind)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := os.Stat("/proc/self/ns/" + kind)
		if err != nil || os.SameFile(original, actual) {
			t.Fatal("private namespace required", kind, err)
		}
	}
	original, err := os.Stat("/proc/1/ns/pid")
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.Stat("/proc/self/ns/pid")
	if err != nil || !os.SameFile(original, actual) {
		t.Fatal("original PID namespace required", err)
	}
	base := t.TempDir()
	etc := filepath.Join(base, "etc")
	if err = os.Mkdir(etc, 0755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(etc, "superbench")
	if err = os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	p := hoststorage.Policy{OperationJournal: "indexed-v2", Root: filepath.Join(base, "data"), Ledger: filepath.Join(base, "ledger"), BenchBytes: 1024, StoreBytes: 1024, MirrorBytes: 1024, ReserveBytes: 1024, BenchInodes: 1, StoreInodes: 1, MirrorInodes: 1, ReserveInodes: 1}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "storage.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = syscall.Mount(etc, "/etc", "", syscall.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	var pins []*policyPin
	defer func() {
		// Release the fixture's retained mount descriptors before unmounting its
		// private /etc. Testing cleanups run after ordinary deferred functions.
		for _, pin := range pins {
			pin.close()
		}
		if err := syscall.Unmount("/etc", 0); err != nil {
			t.Error(err)
		}
	}()
	resolve := func() *policyPin {
		t.Helper()
		pin, got, e := installedPolicy()
		if e != nil || got.Root != p.Root {
			t.Fatalf("fixed policy baseline: %+v %v", got, e)
		}
		pins = append(pins, pin)
		if e = pin.check(context.Background()); e != nil {
			t.Fatal(e)
		}
		return pin
	}
	pin := resolve()
	if err = os.Rename(path, path+".retained"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = pin.check(context.Background()); !errors.Is(err, ErrPolicy) {
		t.Fatalf("identical replacement inherited authority: %v", err)
	}
	pin = resolve()
	leaf, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(dir, dir+".retained"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(dir+".retained", "storage.json"), path); err != nil {
		t.Fatal(err)
	}
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(leaf, named) {
		t.Fatal("fixture did not preserve exact leaf", err)
	}
	if err = pin.check(context.Background()); !errors.Is(err, ErrPolicy) {
		t.Fatalf("changed ancestor inherited authority: %v", err)
	}
	pin = resolve()
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if next, _, e := installedPolicy(); !errors.Is(e, ErrPolicy) || next != nil {
		t.Fatalf("FIFO policy not promptly refused: %v", e)
	}
	if err = pin.check(context.Background()); !errors.Is(err, ErrPolicy) {
		t.Fatalf("FIFO replacement retained authority: %v", err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	resolve()
	if err = os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if next, _, e := installedPolicy(); !errors.Is(e, ErrPolicy) || next != nil {
		t.Fatalf("writable ancestor accepted: %v", e)
	}
	if err = os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	resolve()
	t.Log("INDEXED_POLICY_FIXED_FD_LEAF_ANCESTOR_FIFO_REFUSALS_PASS")
}

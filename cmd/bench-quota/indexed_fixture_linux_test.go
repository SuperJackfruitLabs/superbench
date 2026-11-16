//go:build linux && serviceidentityfixture

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationcontrol"
)

// This proves installed-policy refusal and command routing, not successful XFS
// initialization, quota effects, ownership transfer or sudo installation.
func TestInstalledIndexedPolicyRefusesMissingHistory(t *testing.T) {
	if os.Getenv("SUPERBENCH_INDEXED_POLICY_FIXTURE") != "1" {
		t.Skip("requires explicit isolated root fixture")
	}
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		t.Fatal("requires root")
	}
	for _, kind := range []string{"mnt", "net"} {
		a, err := os.Stat("/proc/1/ns/" + kind)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.Stat("/proc/self/ns/" + kind)
		if err != nil || os.SameFile(a, b) {
			t.Fatal("requires private namespace", kind, err)
		}
	}
	a, err := os.Stat("/proc/1/ns/pid")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat("/proc/self/ns/pid")
	if err != nil || !os.SameFile(a, b) {
		t.Fatal("requires original PID namespace", err)
	}
	base := t.TempDir()
	if err = os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	privateEtc := filepath.Join(base, "etc")
	if err = os.MkdirAll(filepath.Join(privateEtc, "superbench"), 0755); err != nil {
		t.Fatal(err)
	}
	p := hoststorage.Policy{OperationJournal: "indexed-v2", Root: filepath.Join(base, "data"), Ledger: filepath.Join(base, "ledger"), OwnerUID: 1001, OwnerGID: 1001,
		BenchBytes: 1024, StoreBytes: 1024, MirrorBytes: 1024, ReserveBytes: 1024, BenchInodes: 10, StoreInodes: 10, MirrorInodes: 10, ReserveInodes: 10}
	if err = os.Mkdir(p.Root, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(p.Ledger, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(privateEtc, "superbench", "storage.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = syscall.Mount(privateEtc, "/etc", "", syscall.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := syscall.Unmount("/etc", 0); err != nil {
			t.Error(err)
		}
	}()
	want := p.Ledger + ".format-v2"
	missingHistory := func(err error) {
		t.Helper()
		var path *os.PathError
		if !errors.As(err, &path) || path.Path != want || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("not refused at missing retained format: %v", err)
		}
	}
	c, actual, err := quotaoperationcontrol.Open(context.Background())
	missingHistory(err)
	if c != nil || actual.Root != "" {
		t.Fatal("partial authority escaped")
	}
	var output bytes.Buffer
	effects := 0
	err = runFixedPolicy(context.Background(), quotaoperationcontrol.PolicyPath, p, func(w io.Writer) error { effects++; _, err := w.Write([]byte("unverified")); return err }, &output)
	missingHistory(err)
	err = runIndexedOperation(context.Background(), p, operationRequest{command: "operation-info"}, "", "", &output)
	missingHistory(err)
	missingHistory(run([]string{"apply", "bench", "fresh"}))
	if effects != 0 || output.Len() != 0 {
		t.Fatal("missing indexed history fell back or published output", effects, output.String())
	}
	entries, err := os.ReadDir(p.Ledger)
	if err != nil || len(entries) != 0 {
		t.Fatal("refusal created journal or allocation metadata", err)
	}
	if _, err = os.Lstat(filepath.Join(p.Root, "benches")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refusal reached quota allocation", err)
	}
	t.Log("INSTALLED_INDEXED_POLICY_MISSING_HISTORY_NO_FALLBACK")
}

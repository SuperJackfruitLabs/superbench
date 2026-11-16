//go:build linux && freshstoragefixture

package payloadbootstrap_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationjournal"
)

// This exercises only the storage primitive under the actual control lock.
// Its synthetic failing initializer is not bootstrap provenance, operator
// approval, a stopped-systemd gate, or the future maintenance command.
func assertFreshAbsentRetirement(t *testing.T, control *quotaoperationjournal.Control, p hoststorage.Policy) {
	t.Helper()
	var a hoststorage.Assignment
	failure := errors.New("fixture stops before fresh ownership initialization")
	err := control.Run(t.Context(), func() error {
		_, err := p.ApplyFreshRequested("retire-empty", 4<<20, func() error { return nil }, func(got hoststorage.Assignment) error { a = got; return failure })
		return err
	})
	if !errors.Is(err, failure) || a.ProjectID < 10000 {
		t.Fatalf("fresh lower allocation not retained: %+v %v", a, err)
	}
	ledgerPath := filepath.Join(p.Ledger, "assignments.json")
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	root := mustLowerInfo(t, a.Path)
	err = control.Run(t.Context(), func() error { _, err := freshretirement.ObserveAbsent(t.Context(), p, "retire-empty"); return err })
	if !errors.Is(err, freshretirement.ErrRetirement) || !os.SameFile(root, mustLowerInfo(t, a.Path)) {
		t.Fatalf("present root was adopted/deleted: %v", err)
	}
	assertLowerBytes(t, ledgerPath, before)
	// An unlinked open file still consumes quota even after the fixed empty
	// root is removed. Removal here is fixture setup, never production authority.
	f, err := os.OpenFile(filepath.Join(a.Path, "held"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.Write([]byte("open-but-unlinked quota evidence")); err != nil {
		t.Fatal(err)
	}
	if err = f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(f.Name()); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(a.Path); err != nil {
		t.Fatal(err)
	}
	err = control.Run(t.Context(), func() error { _, err := freshretirement.ObserveAbsent(t.Context(), p, "retire-empty"); return err })
	if !errors.Is(err, freshretirement.ErrRetirement) {
		t.Fatalf("open unlinked usage released: %v", err)
	}
	assertLowerBytes(t, ledgerPath, before)
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	// Model partial limit initialization: inode cap exists but byte cap is zero.
	// Both reports must still uniquely identify actual zero usage; empty output
	// would be a refusal. Production retirement never changes any kernel limit.
	cmd := exec.Command("/usr/sbin/xfs_quota", "-x", "-c", fmt.Sprintf("limit -p bhard=0 %d", a.ProjectID), p.Root)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("owned partial-limit fixture failed: %s %v", out, err)
	}
	// A same-filesystem child bind must not turn a hidden real leaf into an
	// apparent absence. Observe has no old parent approval to catch this for it.
	shadow := filepath.Join(p.Root, "retirement-shadow")
	if err := os.Mkdir(shadow, 0700); err != nil {
		t.Fatal(err)
	}
	benches := filepath.Join(p.Root, "benches")
	if err := syscall.Mount(shadow, benches, "", syscall.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	err = control.Run(t.Context(), func() error { _, err := freshretirement.ObserveAbsent(t.Context(), p, "retire-empty"); return err })
	unmountErr := syscall.Unmount(benches, 0)
	if !errors.Is(err, freshretirement.ErrRetirement) || unmountErr != nil {
		t.Fatalf("hidden child mount accepted: %v unmount%v", err, unmountErr)
	}
	var approval freshretirement.Approval
	err = control.Run(t.Context(), func() error {
		var err error
		approval, err = freshretirement.ObserveAbsent(t.Context(), p, "retire-empty")
		return err
	})
	if err != nil || approval.Assignment != a {
		t.Fatalf("actual absent zero-usage project refused: %+v %v", approval, err)
	}
	assertLowerBytes(t, ledgerPath, before)
	foreign := approval
	foreign.Parent.Inode++
	err = control.Run(t.Context(), func() error { _, err := freshretirement.RetireAbsent(t.Context(), p, foreign); return err })
	if !errors.Is(err, freshretirement.ErrRetirement) {
		t.Fatalf("foreign parent approved: %v", err)
	}
	assertLowerBytes(t, ledgerPath, before)
	if err = os.Mkdir(a.Path, 0700); err != nil {
		t.Fatal(err)
	}
	err = control.Run(t.Context(), func() error { _, err := freshretirement.RetireAbsent(t.Context(), p, approval); return err })
	if !errors.Is(err, freshretirement.ErrRetirement) {
		t.Fatalf("reappeared root released: %v", err)
	}
	if err = os.Remove(a.Path); err != nil {
		t.Fatal(err)
	}
	var tombstone hoststorage.Assignment
	err = control.Run(t.Context(), func() error {
		var err error
		tombstone, err = freshretirement.RetireAbsent(t.Context(), p, approval)
		return err
	})
	if err != nil || tombstone.Path != a.Path || tombstone.ProjectID != a.ProjectID || tombstone.Bytes != 0 || tombstone.Inodes != 0 {
		t.Fatalf("actual retirement failed: %+v %v", tombstone, err)
	}
	retired, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var expected allocationledger.Ledger
	if err := json.Unmarshal(before, &expected); err != nil {
		t.Fatal(err)
	}
	expected.Assignments["bench:retire-empty"] = tombstone
	expectedRaw, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	assertLowerBytes(t, ledgerPath, expectedRaw)
	info := mustLowerInfo(t, ledgerPath)
	err = control.Run(t.Context(), func() error {
		again, err := freshretirement.RetireAbsent(t.Context(), p, approval)
		if err == nil && again != tombstone {
			t.Fatalf("lost-reply retry changed tombstone: %+v", again)
		}
		return err
	})
	if err != nil || !os.SameFile(info, mustLowerInfo(t, ledgerPath)) {
		t.Fatalf("retirement retry rewrote/adopted reservation: %v", err)
	}
	assertLowerBytes(t, ledgerPath, retired)
	err = control.Run(t.Context(), func() error { _, err := p.ApplyRequested("bench", "retire-empty", 0); return err })
	if !errors.Is(err, hoststorage.ErrRegisteredAllocation) {
		t.Fatalf("legacy resurrected Fresh tombstone: %v", err)
	}
	if _, err = os.Lstat(a.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired fixed leaf recreated: %v", err)
	}
	assertLowerBytes(t, ledgerPath, retired)
	t.Log("LOWER_FRESH_ABSENT_RETIREMENT_AND_RETRY_PASS")
}

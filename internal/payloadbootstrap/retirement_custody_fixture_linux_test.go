//go:build linux && freshstoragefixture

package payloadbootstrap

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRetirementApprovalCustodyFixture(t *testing.T) {
	if os.Getenv("SUPERBENCH_FRESH_LOWER_FIXTURE") != "1" || os.Getuid() != 0 || os.Geteuid() != 0 {
		t.Fatal("dedicated direct-root lower fixture required")
	}
	trial := os.Getenv("SUPERBENCH_LOWER_TRIAL")
	if trial == "" || !filepath.IsAbs(trial) {
		t.Fatal("missing lower trial")
	}
	ledger := filepath.Join(trial, "retirement-approval-custody")
	if err := os.Mkdir(ledger, 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"approved":"exact retained input"}`)
	if err := retainRetirementFile(ledger, "card", raw); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ledger, "payload-retirements", "card.json")
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err := retainRetirementFile(ledger, "card", raw); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
		t.Fatal("retry rewrote approval", err)
	}
	if err := retainRetirementFile(ledger, "card", []byte("different")); !errors.Is(err, ErrBootstrap) {
		t.Fatal("different approval accepted", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("approval changed", err)
	}
	for _, kind := range []string{"symlink", "hardlink", "partial", "mode", "directory"} {
		bad := filepath.Join(ledger, "payload-retirements", kind+".json")
		switch kind {
		case "symlink":
			err = os.Symlink(path, bad)
		case "hardlink":
			err = os.Link(path, bad)
		case "partial":
			err = os.WriteFile(bad, []byte("{"), 0600)
		case "mode":
			err = os.WriteFile(bad, raw, 0644)
		case "directory":
			err = os.Mkdir(bad, 0700)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := retainRetirementFile(ledger, kind, raw); err == nil {
			t.Fatal("unsafe approval accepted", kind)
		}
		if kind == "hardlink" {
			if err := os.Remove(bad); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("refusal changed original", err)
	}
	t.Log("RETIREMENT_APPROVAL_CUSTODY_PASS")
}

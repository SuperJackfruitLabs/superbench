//go:build linux

package hoststorage

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadOnlyPolicyRefusalCreatesNoLedgerAnchorOrBench(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := testPolicy()
	p.Root = filepath.Join(root, "data")
	p.Ledger = filepath.Join(root, "ledger")
	if err = os.Mkdir(p.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = p.VerifyStorage(context.Background(), "source"); err == nil {
		t.Fatal("missing ledger accepted")
	}
	for _, dir := range []string{p.Root, root} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if dir == root {
			want = 1
		}
		if len(entries) != want {
			t.Fatal("refusal created storage authority", dir, entries)
		}
	}
}

// The disposable managed drill supplies an already allocated, deliberately
// absent bench. This checks the actual XFS helper inspection path, not a mock.
func TestExistingManagedStorageVerificationIsReadOnly(t *testing.T) {
	policy, key := os.Getenv("SUPERBENCH_STORAGE_VERIFY_POLICY"), os.Getenv("SUPERBENCH_STORAGE_VERIFY_KEY")
	if policy == "" || key == "" {
		t.Skip("requires disposable managed XFS fixture")
	}
	p, err := Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath, anchorPath := filepath.Join(p.Ledger, "assignments.json"), filepath.Join(p.Root, ".superbench-quota-owner.json")
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	rootEntries, err := os.ReadDir(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := p.VerifyStorage(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(proof.Assignment.Path); !os.IsNotExist(err) {
		t.Fatal("fixture must have an absent allocated bench", err)
	}
	if _, err = p.VerifyStorage(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err = p.VerifyStorage(context.Background(), "not-allocated"); err == nil {
		t.Fatal("missing ledger assignment accepted")
	}
	after, _ := os.ReadFile(ledgerPath)
	afterAnchor, _ := os.ReadFile(anchorPath)
	afterEntries, _ := os.ReadDir(p.Root)
	if string(before) != string(after) || string(anchor) != string(afterAnchor) || !reflect.DeepEqual(rootEntries, afterEntries) {
		t.Fatal("read-only verification changed ledger, anchor or root directories")
	}
	if _, err = os.Lstat(proof.Assignment.Path); !os.IsNotExist(err) {
		t.Fatal("inspection recreated missing bench", err)
	}
}

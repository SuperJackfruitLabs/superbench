//go:build linux

package quotaoperationcontrol

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPinnedPolicyReadsOriginalDescriptorAndRejectsIdenticalReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy")
	raw := []byte(`{"Root":"original"}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	original, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path, path+".retained"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if samePolicyNode(original, replacement, true) {
		t.Fatal("byte-identical replacement inherited authority")
	}
	if err = os.WriteFile(path, []byte(`{"Root":"redirected"}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readPolicyFD(f)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("policy read redirected: %q %v", got, err)
	}
}
func TestPinnedPolicyRejectsInPlaceMutationAndOversizedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("changed-size"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if samePolicyNode(before, after, true) {
		t.Fatal("modified policy retained authority")
	}
	if err = os.WriteFile(path, bytes.Repeat([]byte{'x'}, (64<<10)+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readPolicyFD(f); !errors.Is(err, ErrPolicy) {
		t.Fatalf("unbounded policy: %v", err)
	}
}

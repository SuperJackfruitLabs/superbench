package storagecustody

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Private owner-aware seams let ordinary fixtures exercise the algorithm. Public
// entrypoints remain fixed UID0; these tests do not claim privileged XFS proof.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}
func exactError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("error=%v want=%q", err, want)
	}
}

func TestPhysicalRootPreservesIdentityWithoutOwnerRequirement(t *testing.T) {
	root := fixtureRoot(t)
	dev, ino, err := PhysicalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if dev != uint64(st.Dev) || ino != st.Ino {
		t.Fatalf("identity mismatch:%d/%d", dev, ino)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("observation mutated root:%v %v", entries, err)
	}
	for _, path := range []string{"relative", "/", root + "/../" + filepath.Base(root)} {
		_, _, err = PhysicalRoot(path)
		exactError(t, err, "storage root must be an existing physical absolute directory")
	}
	link := root + "-link"
	if err = os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}
	})
	_, _, err = PhysicalRoot(link)
	exactError(t, err, "storage root contains a symlink")
	_, _, err = PhysicalRoot(filepath.Join(root, "missing"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing root:%v", err)
	}
	file := filepath.Join(root, "file")
	if err = os.WriteFile(file, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = PhysicalRoot(file)
	exactError(t, err, "storage root is not a directory")
	raw, err := os.ReadFile(file)
	if err != nil || string(raw) != "retained" {
		t.Fatalf("file changed:%q %v", raw, err)
	}
}

func TestFixtureOwnerDirectoryAndAncestorRules(t *testing.T) {
	root := fixtureRoot(t)
	leaf := filepath.Join(root, "ledger")
	owner := uint32(os.Geteuid())
	if err := os.Mkdir(leaf, 0700); err != nil {
		t.Fatal(err)
	}
	if err := rootOwnedDirectory(leaf, true, owner); err != nil {
		t.Fatalf("valid fixture:%v", err)
	}
	if err := trustedAncestors(leaf, owner); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(leaf)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(leaf, 0750); err != nil {
		t.Fatal(err)
	}
	if err = rootOwnedDirectory(leaf, false, owner); err != nil {
		t.Fatalf("nonprivate read-only group permission changed:%v", err)
	}
	exactError(t, rootOwnedDirectory(leaf, true, owner), "privileged ledger directory must be private")
	if err = os.Chmod(leaf, 0770); err != nil {
		t.Fatal(err)
	}
	exactError(t, rootOwnedDirectory(leaf, false, owner), leaf+" must be a root-owned directory without group/world write")
	if err = os.Chmod(leaf, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0770); err != nil {
		t.Fatal(err)
	}
	exactError(t, trustedAncestors(leaf, owner), "privileged path has a writable non-sticky ancestor")
	if err = os.Chmod(root, os.ModeSticky|0777); err != nil {
		t.Fatal(err)
	}
	if err = trustedAncestors(leaf, owner); err != nil {
		t.Fatalf("sticky ancestor algorithm changed:%v", err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(leaf)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("checks replaced fixture:%v", err)
	}
	if owner != 0 {
		err = RootOwnedDirectory(leaf, true)
		if err == nil || !strings.Contains(err.Error(), "untrusted ancestor") {
			t.Fatalf("public root boundary accepted fixture owner:%v", err)
		}
	}
}

func TestAncestorAndDirectorySymlinksRefuseWithoutRepair(t *testing.T) {
	root := fixtureRoot(t)
	owner := uint32(os.Geteuid())
	actual := filepath.Join(root, "actual")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	exactError(t, trustedAncestors(filepath.Join(link, "leaf"), owner), "privileged path has an untrusted ancestor")
	exactError(t, rootOwnedDirectory(link, true, owner), link+" must be a root-owned directory without group/world write")
	target, err := os.Readlink(link)
	if err != nil || target != actual {
		t.Fatalf("link changed:%q %v", target, err)
	}
	entries, err := os.ReadDir(actual)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refusal materialized children:%v %v", entries, err)
	}
	err = rootOwnedDirectory(filepath.Join(actual, "absent"), true, owner)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory:%v", err)
	}
}

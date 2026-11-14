package reap

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func permissionFixture(t *testing.T) (string, Tree, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "archive")
	if e := os.MkdirAll(filepath.Join(root, "nested"), 0700); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"file", "nested/output"} {
		if e := os.WriteFile(filepath.Join(root, name), []byte("retained"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	target := filepath.Join(base, "outside")
	if e := os.WriteFile(target, []byte("external"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return os.Chmod(path, 0700)
			}
			return nil
		})
	})
	tree, e := Snapshot(root, nil)
	if e != nil {
		t.Fatal(e)
	}
	return root, tree, target
}

func TestFreezePreservesFilesLinksAndReadAccess(t *testing.T) {
	root, tree, target := permissionFixture(t)
	if e := Freeze(root, tree); e != nil {
		t.Fatal(e)
	}
	frozen, e := Frozen(root, tree)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range append([]Entry{frozen.Root}, frozen.Entries...) {
		if os.FileMode(v.Mode)&os.ModeSymlink == 0 && v.Mode&0222 != 0 {
			t.Fatal("write bits retained", v)
		}
	}
	if raw, e := os.ReadFile(filepath.Join(root, "nested/output")); e != nil || string(raw) != "retained" {
		t.Fatal(string(raw), e)
	}
	info, e := os.Stat(target)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("symlink target permissions changed", e)
	}
	if os.Geteuid() != 0 {
		if e = os.WriteFile(filepath.Join(root, "file"), []byte("changed"), 0600); e == nil {
			t.Fatal("archived file writable")
		}
		if e = os.WriteFile(filepath.Join(root, "new"), nil, 0600); e == nil {
			t.Fatal("archived directory writable")
		}
	}
	if e = Freeze(root, tree); e != nil {
		t.Fatal("idempotent freeze failed", e)
	}
}

func TestFreezeResumesOnlyIntendedModeChanges(t *testing.T) {
	for _, change := range []string{"none", "new", "edited", "linked"} {
		t.Run(change, func(t *testing.T) {
			root, tree, _ := permissionFixture(t)
			cut := errors.New("interrupt after first mode change")
			if e := freeze(root, tree, func() error { return cut }); !errors.Is(e, cut) {
				t.Fatal(e)
			}
			switch change {
			case "new":
				if e := os.WriteFile(filepath.Join(root, "new"), []byte("keep"), 0600); e != nil {
					t.Fatal(e)
				}
			case "edited":
				if e := os.Chmod(filepath.Join(root, "nested/output"), 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(filepath.Join(root, "nested/output"), []byte("changed"), 0600); e != nil {
					t.Fatal(e)
				}
			case "linked":
				if e := os.Link(filepath.Join(root, "file"), filepath.Join(filepath.Dir(root), "alias")); e != nil {
					t.Fatal(e)
				}
			}
			e := Freeze(root, tree)
			if change == "none" && e != nil || change != "none" && e == nil {
				t.Fatal("incorrect freeze recovery", change, e)
			}
		})
	}
}

func TestHardLinksRefuseBeforeAnyModeChange(t *testing.T) {
	root, tree, _ := permissionFixture(t)
	if e := os.Link(filepath.Join(root, "file"), filepath.Join(filepath.Dir(root), "alias")); e != nil {
		t.Fatal(e)
	}
	if e := FreezePreflight(root, tree); e == nil {
		t.Fatal("external hardlink accepted")
	}
	if e := Freeze(root, tree); e == nil {
		t.Fatal("linked archive frozen")
	}
	actual, e := Snapshot(root, nil)
	if e != nil || actual.Root != tree.Root {
		t.Fatal("preflight changed root", e)
	}
	for i, v := range actual.Entries {
		if v != tree.Entries[i] {
			t.Fatal("preflight changed entry", v)
		}
	}
}

func TestFrozenDeletionResumesDirectoryThawWithoutAdoptingNewFiles(t *testing.T) {
	root, tree, target := permissionFixture(t)
	if e := Freeze(root, tree); e != nil {
		t.Fatal(e)
	}
	frozen, e := Frozen(root, tree)
	if e != nil {
		t.Fatal(e)
	}
	// Simulate a crash after the durable deletion intent reopened the root.
	if e = os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "new-work"), []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = DeleteFrozenContents(root, frozen); e == nil {
		t.Fatal("new work deleted")
	}
	if raw, e := os.ReadFile(filepath.Join(root, "new-work")); e != nil || string(raw) != "keep" {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(root, "new-work")); e != nil {
		t.Fatal(e)
	}
	if e = DeleteFrozenContents(root, frozen); e != nil {
		t.Fatal(e)
	}
	remaining, e := os.ReadDir(root)
	if e != nil || len(remaining) != 0 {
		t.Fatal(remaining, e)
	}
	if raw, e := os.ReadFile(target); e != nil || string(raw) != "external" {
		t.Fatal("outside target deleted", e)
	}
}

package reap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteCannotFollowAnIntermediateDirectoryReplacedAfterValidation(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	dir := filepath.Join(root, "directory")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(dir, "victim")
	put(t, original, "approved bytes also linked outside")
	target := filepath.Join(outside, "victim")
	// Equal inode, mode and content defeat a pathname-only metadata recheck.
	if err := os.Link(original, target); err != nil {
		t.Fatal(err)
	}
	plan, err := Snapshot(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	swapped := false
	err = deleteContents(root, plan, func(e Entry) error {
		if e.Path != "directory/victim" || swapped {
			return nil
		}
		swapped = true
		if err := os.Rename(dir, filepath.Join(root, "retained")); err != nil {
			return err
		}
		return os.Symlink(outside, dir)
	})
	if !swapped || err == nil || !strings.Contains(err.Error(), "reap entry changed before removal") {
		t.Fatal("replacement did not reach confined deletion guard", err)
	}
	for _, path := range []string{target, filepath.Join(root, "retained/victim")} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "approved bytes also linked outside" {
			t.Fatal("replacement deleted unapproved or retained bytes", path, err)
		}
	}
}

func TestDeleteRefusesNamedRootReplacementWithoutDeletingEitherTree(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, "victim"), "preserve original")
	plan, err := Snapshot(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	swapped := false
	err = deleteContents(root, plan, func(Entry) error {
		if swapped {
			return nil
		}
		swapped = true
		if err := os.Rename(root, filepath.Join(parent, "retained")); err != nil {
			return err
		}
		if err := os.Mkdir(root, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, "victim"), []byte("preserve replacement"), 0600)
	})
	if !swapped || err == nil || !strings.Contains(err.Error(), "reap root identity changed") {
		t.Fatal("root replacement did not reach named identity guard", err)
	}
	for name, want := range map[string]string{"root": "preserve replacement", "retained": "preserve original"} {
		data, err := os.ReadFile(filepath.Join(parent, name, "victim"))
		if err != nil || string(data) != want {
			t.Fatal("root replacement changed bytes", name, err)
		}
	}
}

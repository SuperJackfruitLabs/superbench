//go:build linux

package storagecustody

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLegacyReaderRegularBoundsAndOwner(t *testing.T) {
	root := fixtureRoot(t)
	path := filepath.Join(root, "authority")
	owner := uint32(os.Geteuid())
	if err := os.WriteFile(path, []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := readProtected(path, 4, owner)
	if err != nil || string(raw) != "1234" {
		t.Fatalf("valid bound:%q %v", raw, err)
	}
	_, err = readProtected(path, 3, owner)
	exactError(t, err, "storage authority exceeds size limit")
	_, err = openProtected(path, owner+1)
	exactError(t, err, "storage authority must be a protected root-owned regular file")
	if owner != 0 {
		_, err = ReadProtected(path, 4)
		exactError(t, err, "storage authority must be a protected root-owned regular file")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatalf("reader changed inode/mode:%v", err)
	}
	raw, err = os.ReadFile(path)
	if err != nil || string(raw) != "1234" {
		t.Fatalf("reader changed bytes:%q %v", raw, err)
	}
}

func TestLegacyReaderSpecialFilesAndWritableMode(t *testing.T) {
	for _, kind := range []string{"writable", "symlink", "fifo", "directory", "missing"} {
		t.Run(kind, func(t *testing.T) {
			root := fixtureRoot(t)
			path := filepath.Join(root, "authority")
			owner := uint32(os.Geteuid())
			if err := os.WriteFile(path, []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			baseline, err := readProtected(path, 8, owner)
			if err != nil || string(baseline) != "retained" {
				t.Fatalf("valid baseline:%q %v", baseline, err)
			}
			saved := filepath.Join(root, "retained-original")
			if err = os.Rename(path, saved); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "writable":
				if err = os.WriteFile(path, []byte("retained"), 0600); err == nil {
					err = os.Chmod(path, 0666)
				}
			case "symlink":
				err = os.Symlink(saved, path)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = readProtected(path, 8, owner)
			switch kind {
			case "symlink":
				if !errors.Is(err, syscall.ELOOP) {
					t.Fatalf("symlink:%v", err)
				}
			case "missing":
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing:%v", err)
				}
			default:
				exactError(t, err, "storage authority must be a protected root-owned regular file")
			}
			raw, err := os.ReadFile(saved)
			if err != nil || string(raw) != "retained" {
				t.Fatalf("original changed:%q %v", raw, err)
			}
			if kind == "fifo" {
				info, err := os.Lstat(path)
				if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
					t.Fatalf("fifo repaired:%v", err)
				}
			}
			if kind == "symlink" {
				target, err := os.Readlink(path)
				if err != nil || target != saved {
					t.Fatalf("symlink repaired:%q %v", target, err)
				}
			}
		})
	}
}

func TestLegacyReaderDoesNotGainNewHardlinkOrAncestorRestrictions(t *testing.T) {
	root := fixtureRoot(t)
	path := filepath.Join(root, "authority")
	owner := uint32(os.Geteuid())
	if err := os.WriteFile(path, []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "hardlink")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Sys().(*syscall.Stat_t).Nlink != 2 {
		t.Fatal("hardlink fixture not established")
	}
	raw, err := readProtected(alias, 6, owner)
	if err != nil || string(raw) != "legacy" {
		t.Fatalf("legacy hardlink semantics changed:%q %v", raw, err)
	}
	link := root + "-alias"
	if err = os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}
	})
	raw, err = readProtected(filepath.Join(link, "authority"), 6, owner)
	if err != nil || string(raw) != "legacy" {
		t.Fatalf("legacy ancestor traversal changed:%q %v", raw, err)
	}
}

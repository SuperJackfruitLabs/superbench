//go:build linux

package cleanworker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func testPurger(project func(fd int) (uint32, error)) *purger {
	if project == nil {
		project = func(int) (uint32, error) { return 10000, nil }
	}
	return &purger{uid: uint32(os.Getuid()), project: 10000, limit: purgeSteps, projectOf: project}
}

func targetOf(t *testing.T, path string) Target {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	return Target{Device: st.Dev, Inode: st.Ino, ProjectID: 10000}
}

func write(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// outside holds what a hostile tree points at; a purge must leave it whole.
func outside(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "outside")
	write(t, filepath.Join(dir, "kept"), "keep", 0o600)
	write(t, filepath.Join(dir, "sub", "kept"), "keep", 0o600)
	return dir
}

func assertOutside(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"kept", "sub/kept"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(raw) != "keep" {
			t.Fatalf("purge reached outside the tree: %s %q %v", name, raw, err)
		}
	}
}

func assertEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("tree not emptied: %d entries %v", len(entries), err)
	}
}

// hostile is everything a payload can leave: symlinks out of the tree, a hard
// link to an outside file, a FIFO, a socket, unreadable files and directories,
// a deep chain, a wide directory and a name the purge itself would use.
func hostile(t *testing.T, root, out string) {
	t.Helper()
	write(t, filepath.Join(root, "file"), "x", 0o600)
	write(t, filepath.Join(root, "unreadable"), "x", 0)
	for name, target := range map[string]string{"file-link": filepath.Join(out, "kept"), "dir-link": out, "dangling": "/nonexistent", "self": "."} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(out, "kept"), filepath.Join(root, "hard")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mknod(filepath.Join(root, "socket"), unix.S_IFSOCK|0o600, 0); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "closed", "a", "b"), "x", 0o600)
	write(t, filepath.Join(root, "writeonly", "c"), "x", 0)
	write(t, filepath.Join(root, liftName(1), "d"), "x", 0o600)
	for i := range 2000 {
		write(t, filepath.Join(root, "wide", fmt.Sprintf("f%04d", i)), "x", 0o600)
	}
	// Three times the descriptor bound, each level holding a file.
	deep := filepath.Join(root, "deep")
	for range purgeDepth*3 + 5 {
		deep = filepath.Join(deep, "d")
		write(t, filepath.Join(deep, "f"), "x", 0o600)
	}
	for _, chmod := range []struct {
		path string
		mode os.FileMode
	}{{filepath.Join(root, "closed", "a"), 0}, {filepath.Join(root, "closed"), 0}, {filepath.Join(root, "writeonly"), 0o300}} {
		if err := os.Chmod(chmod.path, chmod.mode); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPurgeEmptiesAHostileTreeWithoutLeavingIt(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("ordinary test requires an unprivileged user")
	}
	out := outside(t)
	root := filepath.Join(t.TempDir(), "card")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	hostile(t, root, out)
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	p := testPurger(nil)
	if err := p.run(root, targetOf(t, root)); err != nil {
		t.Fatal(err)
	}
	assertEmpty(t, root)
	assertOutside(t, out)
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(out, "kept"), &st); err != nil || st.Nlink != 1 {
		t.Fatalf("hard link's inode changed: %d %v", st.Nlink, err)
	}
	if p.lifted == 0 {
		t.Fatal("a chain deeper than the descriptor bound was not lifted")
	}
}

func TestPurgeRefusesBeforeRemovingAnything(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("ordinary test requires an unprivileged user")
	}
	for name, change := range map[string]func(p *purger, root string, target *Target){
		"other-inode":  func(_ *purger, _ string, target *Target) { target.Inode++ },
		"other-device": func(_ *purger, _ string, target *Target) { target.Device++ },
		"other-owner":  func(p *purger, _ string, _ *Target) { p.uid++ },
		"other-project": func(p *purger, _ string, _ *Target) {
			p.projectOf = func(int) (uint32, error) { return 10001, nil }
		},
		"symlink-root": func(_ *purger, root string, _ *Target) {
			if err := os.Rename(root, root+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(root+".real", root); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "card")
			write(t, filepath.Join(root, "work", "f"), "x", 0o600)
			target := targetOf(t, root)
			p := testPurger(nil)
			change(p, root, &target)
			if err := p.run(root, target); !errors.Is(err, ErrWorker) {
				t.Fatalf("purge not refused: %v", err)
			}
			for _, r := range []string{root, root + ".real"} {
				if raw, err := os.ReadFile(filepath.Join(r, "work", "f")); err == nil && string(raw) == "x" {
					return
				}
			}
			t.Fatal("a refused purge removed an entry")
		})
	}
}

// A directory in another project is never entered, even deep in the tree.
func TestPurgeNeverEntersAnotherProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "card")
	write(t, filepath.Join(root, "a", "foreign", "kept"), "keep", 0o600)
	var foreign unix.Stat_t
	if err := unix.Lstat(filepath.Join(root, "a", "foreign"), &foreign); err != nil {
		t.Fatal(err)
	}
	p := testPurger(func(fd int) (uint32, error) {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return 0, err
		}
		if st.Ino == foreign.Ino {
			return 10001, nil
		}
		return 10000, nil
	})
	if err := p.run(root, targetOf(t, root)); !errors.Is(err, ErrWorker) {
		t.Fatalf("foreign project entered: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "a", "foreign", "kept")); err != nil || string(raw) != "keep" {
		t.Fatalf("foreign project changed: %v", err)
	}
}

// A directory swapped for a symlink out of the tree after its lstat is refused,
// nothing outside changes, and the same purge then completes.
func TestPurgeRefusesASwappedDirectoryThenContinues(t *testing.T) {
	out := outside(t)
	root := filepath.Join(t.TempDir(), "card")
	write(t, filepath.Join(root, "victim", "f"), "x", 0o600)
	p := testPurger(nil)
	p.beforeEnter = func(name string) {
		if name != "victim" {
			return
		}
		p.beforeEnter = nil
		if err := os.Rename(filepath.Join(root, "victim"), filepath.Join(root, "moved")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(out, "sub"), filepath.Join(root, "victim")); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.run(root, targetOf(t, root)); !errors.Is(err, errChanged) {
		t.Fatalf("swap not refused: %v", err)
	}
	assertOutside(t, out)
	if err := testPurger(nil).run(root, targetOf(t, root)); err != nil {
		t.Fatal(err)
	}
	assertEmpty(t, root)
	assertOutside(t, out)
}

// A purge stopped by its step bound has only removed entries; the same purge
// then finishes the tree.
func TestPurgeStepBoundStopsAndTheSamePurgeContinues(t *testing.T) {
	root := filepath.Join(t.TempDir(), "card")
	for i := range 200 {
		write(t, filepath.Join(root, "w", fmt.Sprintf("f%03d", i)), "x", 0o600)
	}
	p := testPurger(nil)
	p.limit = 50
	if err := p.run(root, targetOf(t, root)); err == nil || !strings.Contains(err.Error(), "step bound") {
		t.Fatalf("bound not enforced: %v", err)
	}
	left, err := os.ReadDir(filepath.Join(root, "w"))
	if err != nil || len(left) == 0 || len(left) == 200 {
		t.Fatalf("bounded purge progress: %d %v", len(left), err)
	}
	if err = testPurger(nil).run(root, targetOf(t, root)); err != nil {
		t.Fatal(err)
	}
	assertEmpty(t, root)
}

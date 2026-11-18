//go:build linux

package quotaleaf

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// An ordinary temporary directory has project 0, or no fsxattr support at all:
// either way the leaf check refuses it rather than pass.
func TestLeafRefusesDirectoryOutsideProject(t *testing.T) {
	benches := t.TempDir()
	if err := os.Mkdir(filepath.Join(benches, "b1"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Leaf(benches, "b1", 10001); err == nil {
		t.Fatal("leaf outside its project accepted")
	}
	if err := Leaf(benches, "absent", 10001); err != nil {
		t.Fatalf("absent leaf: %v", err)
	}
	if err := os.Symlink(filepath.Join(benches, "b1"), filepath.Join(benches, "link")); err != nil {
		t.Fatal(err)
	}
	if err := Leaf(benches, "link", 10001); !errors.Is(err, syscall.ELOOP) && !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("leaf symlink followed: %v", err)
	}
}

// fake reports a project per inode, so the walk is tested without XFS.
func fake(t *testing.T, outside map[uint64]Attr) {
	old, oldAt := getattr, getattrAt
	t.Cleanup(func() { getattr, getattrAt = old, oldAt })
	getattrAt = func(dir int, name string) (Attr, error) {
		var st syscall.Stat_t
		if err := syscall.Lstat("/proc/self/fd/"+strconv.Itoa(dir)+"/"+name, &st); err != nil {
			return Attr{}, err
		}
		if a, ok := outside[st.Ino]; ok {
			return a, nil
		}
		return Attr{ProjectID: 10001}, nil
	}
	getattr = func(fd int) (Attr, error) {
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			return Attr{}, err
		}
		if a, ok := outside[st.Ino]; ok {
			return a, nil
		}
		return Attr{ProjectID: 10001, Xflags: FlagProjInherit}, nil
	}
}

func inode(t *testing.T, path string) uint64 {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	return st.Ino
}

func tree(t *testing.T) (string, string) {
	root := t.TempDir()
	leaf := filepath.Join(root, "leaf")
	elsewhere := filepath.Join(root, "elsewhere")
	for _, d := range []string{leaf, filepath.Join(leaf, "a", "b"), elsewhere} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(leaf, "a", "b", "f"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "g"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(leaf, "a", "out")); err != nil {
		t.Fatal(err)
	}
	return leaf, elsewhere
}

func walk(t *testing.T, leaf string, limit int) error {
	f, err := os.Open(leaf)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return Tree(f, 10001, limit)
}

func TestTreeRefusesNestedEntryOutsideProject(t *testing.T) {
	leaf, elsewhere := tree(t)
	// The symlink target is outside the project but is never followed.
	fake(t, map[uint64]Attr{inode(t, elsewhere): {}, inode(t, filepath.Join(elsewhere, "g")): {}})
	if err := walk(t, leaf, 16); err != nil {
		t.Fatalf("clean tree: %v", err)
	}
	fake(t, map[uint64]Attr{inode(t, filepath.Join(leaf, "a", "b", "f")): {}})
	if err := walk(t, leaf, 16); !errors.Is(err, ErrTreeProject) {
		t.Fatalf("file moved to project 0: %v", err)
	}
	fake(t, map[uint64]Attr{inode(t, filepath.Join(leaf, "a", "b")): {ProjectID: 10001}})
	if err := walk(t, leaf, 16); !errors.Is(err, ErrTreeProject) {
		t.Fatalf("directory without inherit: %v", err)
	}
	fake(t, map[uint64]Attr{inode(t, leaf): {ProjectID: 10001}})
	if err := walk(t, leaf, 16); !errors.Is(err, ErrLeafProject) {
		t.Fatalf("leaf without inherit: %v", err)
	}
}

func TestTreeIsBounded(t *testing.T) {
	leaf, _ := tree(t)
	fake(t, nil)
	// leaf, a, b, out and f: five inodes.
	if err := walk(t, leaf, 4); !errors.Is(err, ErrTreeLimit) {
		t.Fatalf("five inodes under a cap of four: %v", err)
	}
	if err := walk(t, leaf, 5); err != nil {
		t.Fatalf("five inodes under a cap of five: %v", err)
	}
}

// A chain deeper than any descriptor budget is walked with one directory held
// at a time; a file at its bottom outside the project is still found.
func TestTreeWalksDeepChainsWithoutDescriptorsPerLevel(t *testing.T) {
	leaf := filepath.Join(t.TempDir(), "leaf")
	deep := leaf
	for i := 0; i < 300; i++ {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0700); err != nil {
		t.Fatal(err)
	}
	bottom := filepath.Join(deep, "f")
	if err := os.WriteFile(bottom, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leaf, "after"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	low := limit
	low.Cur = 64 // far fewer than the chain's 300 levels
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &low); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit) })
	fake(t, nil)
	if err := walk(t, leaf, 1000); err != nil {
		t.Fatalf("deep chain: %v", err)
	}
	fake(t, map[uint64]Attr{inode(t, bottom): {}})
	if err := walk(t, leaf, 1000); !errors.Is(err, ErrTreeProject) {
		t.Fatalf("escaped file at the bottom of a deep chain: %v", err)
	}
}

// A symlink is never followed or opened, but its own project is checked where
// the kernel can read it by name; without file_getattr it is only counted.
func TestTreeChecksSymlinksByNameWhereTheKernelCan(t *testing.T) {
	leaf, _ := tree(t)
	link := filepath.Join(leaf, "a", "out")
	fake(t, map[uint64]Attr{inode(t, link): {}})
	if err := walk(t, leaf, 16); !errors.Is(err, ErrTreeProject) {
		t.Fatalf("symlink outside its project: %v", err)
	}
	getattrAt = func(int, string) (Attr, error) { return Attr{}, errNoGetattrAt }
	if err := walk(t, leaf, 16); err != nil {
		t.Fatalf("a kernel without file_getattr: %v", err)
	}
}

// Hard links add names, not inodes: a tree of many links to one file stays
// under the inode cap, and only the separate name bound limits the walk,
// with its own error rather than an escape.
func TestTreeCountsDistinctInodesNotHardLinks(t *testing.T) {
	leaf, _ := tree(t)
	file := filepath.Join(leaf, "a", "b", "f")
	for i := 0; i < 20; i++ {
		if err := os.Link(file, filepath.Join(leaf, "a", "b", "link"+strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	fake(t, nil)
	if err := walk(t, leaf, 5); err != nil {
		t.Fatalf("hard links counted as inodes: %v", err)
	}
	old := maxNames
	t.Cleanup(func() { maxNames = old })
	maxNames = 10
	if err := walk(t, leaf, 5); !errors.Is(err, ErrTreeNames) || errors.Is(err, ErrTreeLimit) {
		t.Fatalf("24 names under a bound of 10: %v", err)
	}
}

// An entry swapped between its O_PATH stat and its open is refused.
func TestTreeRefusesAnEntryReplacedBeforeItsOpen(t *testing.T) {
	leaf, elsewhere := tree(t)
	fake(t, nil)
	t.Cleanup(func() { hooks.beforeReopen = nil })
	hooks.beforeReopen = func(name string) {
		if name != "b" {
			return
		}
		hooks.beforeReopen = nil
		b := filepath.Join(leaf, "a", "b")
		if err := os.Rename(b, filepath.Join(elsewhere, "old-b")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(b, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := walk(t, leaf, 16); !errors.Is(err, ErrTreeProject) {
		t.Fatalf("replaced directory: %v", err)
	}
}

// A directory moved while the walk is inside it returns through ".." to a
// different node, which is refused rather than walked.
func TestTreeRefusesAParentThatChangedUnderTheWalk(t *testing.T) {
	leaf, _ := tree(t)
	if err := os.Mkdir(filepath.Join(leaf, "c"), 0700); err != nil {
		t.Fatal(err)
	}
	fake(t, nil)
	t.Cleanup(func() { hooks.beforeUp = nil })
	b := inode(t, filepath.Join(leaf, "a", "b"))
	hooks.beforeUp = func(cur int) {
		var st syscall.Stat_t
		if syscall.Fstat(cur, &st) != nil || st.Ino != b {
			return
		}
		hooks.beforeUp = nil
		// The walk is in a/b and about to return to a: b now lives under c.
		if err := os.Rename(filepath.Join(leaf, "a", "b"), filepath.Join(leaf, "c", "b")); err != nil {
			t.Fatal(err)
		}
	}
	if err := walk(t, leaf, 16); !errors.Is(err, ErrTreeProject) {
		t.Fatalf("parent changed under the walk: %v", err)
	}
}

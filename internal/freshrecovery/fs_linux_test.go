//go:build linux

package freshrecovery

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
)

// These run unprivileged on a temporary tree. Owners, modes and the XFS project
// are the privileged fixture's; here only shape, identity and removal are tested.

func holdLeaf(t *testing.T) (*os.File, *held, string) {
	t.Helper()
	base := t.TempDir()
	leafPath := filepath.Join(base, "card")
	if err := os.Mkdir(leafPath, 0700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { parent.Close() })
	leaf, st, err := openDir(parent, "card", leafPath)
	if err != nil {
		t.Fatal(err)
	}
	h := &held{Leaf: Leaf{Node: nodeOf(st)}, dirs: map[string]*os.File{".": leaf}}
	t.Cleanup(h.close)
	return parent, h, leafPath
}

func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWalkHoldsOnlyAPrefixOfFixedEmptyDirectories(t *testing.T) {
	fixed := payloadbootstrap.FixedDirectories(testSpec("api"))
	for n := range len(fixed) + 1 {
		_, h, leaf := holdLeaf(t)
		mkdirs(t, leaf, fixed[:n]...)
		if err := h.walk(fixed); err != nil || len(h.Fixed) != n {
			t.Fatalf("prefix %d: %d held %v", n, len(h.Fixed), err)
		}
		for k, e := range h.Fixed {
			info, err := os.Lstat(filepath.Join(leaf, e.Name))
			if err != nil || e.Name != fixed[k] || e.Node.Inode != info.Sys().(*syscall.Stat_t).Ino {
				t.Fatalf("entry %d differs: %+v %v", k, e, err)
			}
		}
	}
	for name, setup := range map[string]func(string){
		"stray file": func(leaf string) { os.WriteFile(filepath.Join(leaf, "stray"), nil, 0600) },
		"file in fixed dir": func(leaf string) {
			mkdirs(t, leaf, fixed[:2]...)
			os.WriteFile(filepath.Join(leaf, fixed[1], "x"), nil, 0600)
		},
		"gap":               func(leaf string) { mkdirs(t, leaf, fixed[1], fixed[2]) },
		"unknown directory": func(leaf string) { mkdirs(t, leaf, fixed[0], ".superbench-other") },
		"fixed name a file": func(leaf string) { os.WriteFile(filepath.Join(leaf, fixed[0]), nil, 0600) },
		"fixed name a link": func(leaf string) { os.Symlink("/", filepath.Join(leaf, fixed[0])) },
		"member checkout":   func(leaf string) { mkdirs(t, leaf, "api") },
	} {
		_, h, leaf := holdLeaf(t)
		setup(leaf)
		if err := h.walk(fixed); !errors.Is(err, ErrRecovery) {
			t.Errorf("%s admitted: %v", name, err)
		}
	}
}

func TestExactlyComparesEntriesAndLinkCount(t *testing.T) {
	_, h, leaf := holdLeaf(t)
	dir := h.dirs["."]
	if err := exactly(dir, nil); err != nil {
		t.Fatal("empty directory refused", err)
	}
	mkdirs(t, leaf, "a", "b")
	if err := exactly(dir, []string{"b", "a"}); err != nil {
		t.Fatal("exact children refused", err)
	}
	for name, want := range map[string][]string{"missing child": {"a"}, "extra child": {"a", "b", "c"}, "empty": nil, "renamed": {"a", "c"}} {
		if err := exactly(dir, want); !errors.Is(err, ErrRecovery) {
			t.Errorf("%s admitted: %v", name, err)
		}
	}
}

func TestRmdirAtRemovesOnlyTheHeldEmptyDirectory(t *testing.T) {
	parent, h, leaf := holdLeaf(t)
	self := h.dirs["."]
	if err := os.WriteFile(filepath.Join(leaf, "kept"), []byte("evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rmdirAt(parent, self, "card", h.Node); !errors.Is(err, ErrRecovery) {
		t.Fatalf("non-empty directory removed: %v", err)
	}
	if err := os.Remove(filepath.Join(leaf, "kept")); err != nil {
		t.Fatal(err)
	}
	other := h.Node
	other.Inode++
	if err := rmdirAt(parent, self, "card", other); !errors.Is(err, ErrRecovery) {
		t.Fatalf("a different planned node removed: %v", err)
	}
	// A swapped name must not be removed in place of the held directory.
	if err := os.Rename(leaf, leaf+".held"); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, filepath.Dir(leaf), "card")
	if err := rmdirAt(parent, self, "card", h.Node); !errors.Is(err, ErrRecovery) {
		t.Fatalf("swapped name removed: %v", err)
	}
	if _, err := os.Lstat(leaf); err != nil {
		t.Fatal("swapped directory removed", err)
	}
	if err := os.Remove(leaf); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(leaf+".held", leaf); err != nil {
		t.Fatal(err)
	}
	if err := rmdirAt(parent, self, "card", h.Node); err != nil {
		t.Fatal("held empty directory not removed", err)
	}
	if _, err := os.Lstat(leaf); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("directory remains", err)
	}
}

func TestObserveRefusesLinksAndFilesAndReportsAbsence(t *testing.T) {
	base := t.TempDir()
	benches, err := os.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer benches.Close()
	if _, err := observe(benches, "card", nil); !errors.Is(err, errAbsent) {
		t.Fatalf("absent leaf: %v", err)
	}
	target := filepath.Join(base, "target")
	mkdirs(t, base, "target")
	if err := os.Symlink(target, filepath.Join(base, "card")); err != nil {
		t.Fatal(err)
	}
	if _, err := observe(benches, "card", nil); !errors.Is(err, ErrRecovery) {
		t.Fatalf("symlinked leaf followed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := observe(benches, "file", nil); !errors.Is(err, ErrRecovery) {
		t.Fatalf("file leaf opened: %v", err)
	}
	removal := slices.Clone(payloadbootstrap.FixedDirectories(testSpec("api")))
	slices.Reverse(removal)
	if removal[0] != ".superbench-runtime/api/tmp" || removal[len(removal)-1] != ".superbench-git" {
		t.Fatalf("reverse creation order changed: %q", removal)
	}
}

// The leaf is already gone: a retry is accepted only when this step's own
// approval was retained, and nothing is created when it was not.
func TestAbsentLeafRetryRequiresTheRetainedApproval(t *testing.T) {
	ledger := t.TempDir()
	if err := retain(ledger, "card", "leaf", []byte(`{"plan":1}`), true); !errors.Is(err, ErrRecovery) {
		t.Fatalf("absent leaf accepted without an approval: %v", err)
	}
	if err := os.Mkdir(filepath.Join(ledger, "payload-cleanups"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ledger, "payload-cleanups", "card.repair.json"), []byte(`{"plan":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := retain(ledger, "card", "leaf", []byte(`{"plan":1}`), true); !errors.Is(err, ErrRecovery) {
		t.Fatalf("another step's approval accepted for the leaf: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(ledger, "payload-cleanups"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("a refused retry created files: %v %v", entries, err)
	}
}

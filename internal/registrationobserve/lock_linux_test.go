//go:build linux

package registrationobserve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func stateFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lock", "bench.db"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("unchanged fixture bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestHeldStateRetainsExistingSharedLockWithoutWrites(t *testing.T) {
	root := stateFixture(t)
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	h, err := holdState(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(root, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != syscall.EWOULDBLOCK {
		t.Fatalf("shared exclusion absent: %v", err)
	}
	if err = h.recheck(); err != nil {
		t.Fatal(err)
	}
	h.close()
	if err = h.recheck(); !errors.Is(err, ErrObservation) {
		t.Fatalf("closed observation usable: %v", err)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("original lock leaked: %v", err)
	}
	after, err := os.ReadDir(root)
	if err != nil || len(before) != len(after) {
		t.Fatalf("inventory changed: %v", err)
	}
	for _, name := range []string{"lock", "bench.db"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(raw) != "unchanged fixture bytes" {
			t.Fatalf("evidence changed: %s %v", name, err)
		}
	}
}

func TestHeldStateRefusesMissingOrUnsafeFilesBeforeAdoption(t *testing.T) {
	for _, name := range []string{"lock", "bench.db"} {
		for _, kind := range []string{"missing", "public", "hardlink", "symlink", "fifo"} {
			t.Run(name+"-"+kind, func(t *testing.T) {
				root := stateFixture(t)
				path := filepath.Join(root, name)
				baseline, err := holdState(context.Background(), root)
				if err != nil {
					t.Fatal(err)
				}
				baseline.close()
				switch kind {
				case "missing":
					err = os.Remove(path)
				case "public":
					err = os.Chmod(path, 0644)
				case "hardlink":
					err = os.Link(path, filepath.Join(root, "retained-link"))
				case "symlink":
					if err = os.Remove(path); err == nil {
						err = os.Symlink("retained-target", path)
					}
				case "fifo":
					if err = os.Remove(path); err == nil {
						err = syscall.Mkfifo(path, 0600)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				before, err := os.Lstat(path)
				if kind != "missing" && err != nil {
					t.Fatal(err)
				}
				h, err := holdState(context.Background(), root)
				if h != nil || !errors.Is(err, ErrObservation) {
					t.Fatalf("unsafe state adopted: %v", err)
				}
				if kind == "missing" {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("wrong missing-file cause: %v", err)
					}
					if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("missing history recreated: %v", err)
					}
				} else {
					if kind == "symlink" && !errors.Is(err, syscall.ELOOP) {
						t.Fatalf("wrong nofollow cause: %v", err)
					}
					after, e := os.Lstat(path)
					if e != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
						t.Fatalf("refusal changed evidence: %v", e)
					}
				}
			})
		}
	}
}

func TestHeldStateDoesNotRecreateMissingLockWhileOriginalFDHeld(t *testing.T) {
	root := stateFixture(t)
	h, err := holdState(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	if err = os.Remove(filepath.Join(root, "lock")); err != nil {
		t.Fatal(err)
	}
	if err = h.recheck(); !errors.Is(err, ErrObservation) {
		t.Fatalf("unlinked lock adopted: %v", err)
	}
	if next, err := holdState(context.Background(), root); next != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing lock replaced: %v", err)
	}
	if _, err = os.Lstat(filepath.Join(root, "lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock created: %v", err)
	}
}

func TestHeldStateRefusesNamedFileReplacementAndHonorsCancellation(t *testing.T) {
	for _, name := range []string{"lock", "bench.db"} {
		t.Run(name, func(t *testing.T) {
			root := stateFixture(t)
			h, err := holdState(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer h.close()
			backup := filepath.Join(t.TempDir(), "original")
			if err = os.Rename(filepath.Join(root, name), backup); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, name), []byte("replacement"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = h.recheck(); !errors.Is(err, ErrObservation) {
				t.Fatalf("replacement adopted: %v", err)
			}
		})
	}
	root := stateFixture(t)
	f, err := os.Open(filepath.Join(root, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if h, err := holdState(ctx, root); h != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait ignored original deadline: %v", err)
	}
}

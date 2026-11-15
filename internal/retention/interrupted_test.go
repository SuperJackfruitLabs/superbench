package retention

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
)

const interruptedID = "1234567890abcdef1234567890abcdef"

func interruptedFixture(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	name, _ := Name(interruptedID)
	return root, filepath.Join(root, name)
}

func TestInterruptedObservationLocksExactBoundedBytesThroughCommit(t *testing.T) {
	root, path := interruptedFixture(t)
	text := bytes.Repeat([]byte("retained output\n"), 20)
	if err := os.WriteFile(path, text, 0600); err != nil {
		t.Fatal(err)
	}
	f, entry, size, err := ObserveInterrupted(root, interruptedID, uint64(len(text)))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want, err := Snapshot(root, interruptedID)
	if err != nil || entry == nil || *entry != want || size != int64(len(text)) {
		t.Fatal("bounded observation differs from retained snapshot", entry, size, err)
	}
	other, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("writer lock was not retained")
	}
	if err := RecheckInterrupted(root, interruptedID, uint64(len(text)), f, entry, size); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, text) {
		t.Fatal("observation changed retained bytes", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal("closing observation did not release writer exclusion", err)
	}
}

func TestInterruptedObservationDistinguishesAbsentAndEmpty(t *testing.T) {
	root, path := interruptedFixture(t)
	f, entry, size, err := ObserveInterrupted(root, interruptedID, 4096)
	if err != nil || f != nil || entry != nil || size != 0 {
		t.Fatal("absent observation", f, entry, size, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("observation created replacement evidence", err)
	}
	if err := RecheckInterrupted(root, interruptedID, 4096, f, entry, size); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := RecheckInterrupted(root, interruptedID, 4096, f, entry, size); err == nil {
		t.Fatal("absence survived a newly created empty log")
	}
	f, entry, size, err = ObserveInterrupted(root, interruptedID, 4096)
	if err != nil || f == nil || entry == nil || size != 0 {
		t.Fatal("existing empty file confused with absence", err)
	}
	defer f.Close()
	if err := RecheckInterrupted(root, interruptedID, 4096, f, entry, size); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedObservationRefusesActiveWriterAndOversizeBeforeHash(t *testing.T) {
	root, path := interruptedFixture(t)
	capture, err := output.Open(path, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if f, _, _, err := ObserveInterrupted(root, interruptedID, 4096); err == nil || f != nil {
		t.Fatal("active product writer accepted", err)
	}
	capture.Close()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if observed, _, _, err := ObserveInterrupted(root, interruptedID, 4096); err == nil || observed != nil || !strings.Contains(err.Error(), "exceeds its recorded limit") {
		t.Fatal("oversize sparse file reached hashing instead of size preflight", err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() != 1<<30 {
		t.Fatal("oversize evidence changed", err)
	}
	// A failed observation must release its descriptor's lock.
	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal("failed observation retained a lock", err)
	}
}

func TestInterruptedRecheckRejectsChangedOrReplacedEvidence(t *testing.T) {
	for _, kind := range []string{"same-inode-bytes", "growth", "shrink", "replacement", "permission", "unlink", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root, path := interruptedFixture(t)
			if err := os.WriteFile(path, []byte("old bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			f, entry, size, err := ObserveInterrupted(root, interruptedID, 4096)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			switch kind {
			case "same-inode-bytes":
				err = os.WriteFile(path, []byte("new bytes"), 0600)
			case "growth":
				err = os.WriteFile(path, []byte("old bytes and more"), 0600)
			case "shrink":
				err = os.WriteFile(path, []byte("old"), 0600)
			case "replacement", "symlink":
				err = os.Rename(path, path+".retained")
				if err == nil && kind == "replacement" {
					err = os.WriteFile(path, []byte("old bytes"), 0600)
				} else if err == nil {
					err = os.Symlink(path+".retained", path)
				}
			case "permission":
				err = os.Chmod(path, 0644)
			case "unlink":
				err = os.Remove(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := RecheckInterrupted(root, interruptedID, 4096, f, entry, size); err == nil {
				t.Fatal("changed interrupted evidence accepted")
			}
		})
	}
}

func TestInterruptedObservationRefusesUnsafeInputsWithoutEffects(t *testing.T) {
	for _, kind := range []string{"relative", "unclean", "missing-parent", "root-symlink", "ancestor-symlink", "public-root", "zero-limit", "large-limit", "invalid-id", "symlink", "dangling", "directory", "fifo", "public-log", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root, path := interruptedFixture(t)
			id, limit := interruptedID, uint64(4096)
			var err error
			switch kind {
			case "relative":
				root = "."
			case "unclean":
				root += "/."
			case "missing-parent":
				root = filepath.Join(root, "missing")
			case "root-symlink", "ancestor-symlink":
				alias := root + "-alias"
				if err = os.Symlink(root, alias); err == nil {
					t.Cleanup(func() { _ = os.Remove(alias) })
					if kind == "ancestor-symlink" {
						err = os.Mkdir(filepath.Join(root, "child"), 0700)
						root = filepath.Join(alias, "child")
					} else {
						root = alias
					}
				}
			case "public-root":
				err = os.Chmod(root, 0755)
			case "zero-limit":
				limit = 0
			case "large-limit":
				limit = (1 << 30) + 1
			case "invalid-id":
				id = "../other"
			case "symlink", "dangling":
				target := filepath.Join(root, "other")
				if kind == "symlink" {
					err = os.WriteFile(target, []byte("preserved"), 0600)
				}
				if err == nil {
					err = os.Symlink(target, path)
				}
			case "directory":
				err = os.Mkdir(path, 0700)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			case "public-log":
				err = os.WriteFile(path, []byte("preserved"), 0644)
			case "hardlink":
				err = os.WriteFile(path, []byte("preserved"), 0600)
				if err == nil {
					err = os.Link(path, filepath.Join(root, "other"))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if f, entry, _, err := ObserveInterrupted(root, id, limit); err == nil || f != nil || entry != nil {
				t.Fatal("unsafe observation accepted", err)
			}
		})
	}
}

func TestInterruptedAbsentRecheckRejectsLeafAppearanceAndMissingParent(t *testing.T) {
	for _, kind := range []string{"dangling", "directory", "parent-removed"} {
		t.Run(kind, func(t *testing.T) {
			root, path := interruptedFixture(t)
			f, entry, size, err := ObserveInterrupted(root, interruptedID, 4096)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "dangling":
				err = os.Symlink("missing-target", path)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "parent-removed":
				err = os.Remove(root)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := RecheckInterrupted(root, interruptedID, 4096, f, entry, size); err == nil {
				t.Fatal("absent leaf lost physical-parent/leaf evidence")
			}
		})
	}
}

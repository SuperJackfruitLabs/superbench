//go:build linux

package activation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const enabled = `{"version":"superbench.activation.v1","cancellation":true}`

// fixture builds base/etc/superbench/activation.json owned by the test user,
// who stands in for root through the owner parameter.
func fixture(t *testing.T, contents string) (string, string) {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(base, "etc", "superbench")
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(base, "etc"), directory} {
		if err := os.Chmod(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(directory, "activation.json")
	if contents != "" {
		if err := os.WriteFile(file, []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(file, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return base, file
}

func owner() uint32 { return uint32(os.Geteuid()) }

func refused(t *testing.T, base string, as uint32, why string) {
	t.Helper()
	if got, err := load(base, fixedNames, as); !errors.Is(err, ErrActivation) || got != (Switches{}) {
		t.Fatalf("%s: accepted %v %v", why, got, err)
	}
}

func TestLoadAbsentFileKeepsExactDefaults(t *testing.T) {
	base, _ := fixture(t, "")
	got, err := load(base, fixedNames, owner())
	if err != nil || got != (Switches{}) || got.Any() {
		t.Fatal("absent file changed the defaults", got, err)
	}
	if err = os.RemoveAll(filepath.Join(base, "etc", "superbench")); err != nil {
		t.Fatal(err)
	}
	if got, err = load(base, fixedNames, owner()); err != nil || got != (Switches{}) {
		t.Fatal("absent directory changed the defaults", got, err)
	}
}

func TestLoadAcceptsTrustedFile(t *testing.T) {
	base, _ := fixture(t, enabled)
	if got, err := load(base, fixedNames, owner()); err != nil || got != (Switches{Cancellation: true}) {
		t.Fatal(got, err)
	}
}

func TestLoadRefusesFileOwnedBySomeoneElse(t *testing.T) {
	base, _ := fixture(t, enabled)
	// The tree is owned by the test user; demanding root (production) or any
	// other owner must refuse.
	refused(t, base, 0, "not root owned")
	refused(t, base, owner()+1, "foreign owner")
	// An unprivileged test cannot chown, so check each node's owner rule on
	// the stat the loader takes: everything else trusted, only the owner differs.
	file := syscall.Stat_t{Mode: syscall.S_IFREG | 0644, Uid: 0, Nlink: 1, Size: 10}
	directory := syscall.Stat_t{Mode: syscall.S_IFDIR | 0755, Uid: 0}
	if !leafOK(&file, 0) || !directoryOK(&directory, 0) {
		t.Fatal("trusted root-owned nodes refused")
	}
	file.Uid, directory.Uid = 1000, 1000
	if leafOK(&file, 0) {
		t.Fatal("accepted a file not owned by root")
	}
	if directoryOK(&directory, 0) {
		t.Fatal("accepted a directory not owned by root")
	}
}

func TestLoadRefusesWritableFile(t *testing.T) {
	for _, mode := range []os.FileMode{0664, 0646, 0666} {
		base, file := fixture(t, enabled)
		if err := os.Chmod(file, mode); err != nil {
			t.Fatal(err)
		}
		refused(t, base, owner(), "mode "+mode.String())
	}
}

func TestLoadRefusesWritableOrForeignParent(t *testing.T) {
	for _, parent := range []string{"etc", "etc/superbench"} {
		base, _ := fixture(t, enabled)
		path := filepath.Join(base, parent)
		if err := os.Chmod(path, 0777); err != nil {
			t.Fatal(err)
		}
		refused(t, base, owner(), "writable "+parent)
		if err := os.Chmod(path, 0775); err != nil {
			t.Fatal(err)
		}
		refused(t, base, owner(), "group-writable "+parent)
	}
	base, _ := fixture(t, enabled)
	if err := os.Chmod(base, 0777); err != nil {
		t.Fatal(err)
	}
	refused(t, base, owner(), "writable base")
}

func TestLoadRefusesSymlinksAndNonRegularFiles(t *testing.T) {
	base, file := fixture(t, "")
	real := filepath.Join(base, "real.json")
	if err := os.WriteFile(real, []byte(enabled), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, file); err != nil {
		t.Fatal(err)
	}
	refused(t, base, owner(), "symlinked file")

	base, _ = fixture(t, enabled)
	moved := filepath.Join(base, "moved")
	if err := os.Rename(filepath.Join(base, "etc", "superbench"), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, filepath.Join(base, "etc", "superbench")); err != nil {
		t.Fatal(err)
	}
	refused(t, base, owner(), "symlinked parent")

	base, file = fixture(t, "")
	if err := syscall.Mkfifo(file, 0644); err != nil {
		t.Fatal(err)
	}
	refused(t, base, owner(), "fifo")

	base, file = fixture(t, enabled)
	if err := os.Link(file, filepath.Join(base, "etc", "second")); err != nil {
		t.Fatal(err)
	}
	refused(t, base, owner(), "hard link")

	base, _ = fixture(t, "{}")
	refused(t, base, owner(), "invalid schema")
}

func TestLoadRefusesUnknownKeyFromTrustedFile(t *testing.T) {
	base, _ := fixture(t, `{"version":"superbench.activation.v1","everything":true}`)
	refused(t, base, owner(), "unknown key")
}

// Only a present file needs trusted ancestors: an absent one keeps the exact
// defaults even under a writable, foreign-owned or private directory.
func TestLoadAbsentFileIgnoresItsDirectories(t *testing.T) {
	for _, parent := range []string{"", "etc", "etc/superbench"} {
		for _, mode := range []os.FileMode{0777, 0775, 0700} {
			base, file := fixture(t, "")
			if err := os.Chmod(filepath.Join(base, parent), mode); err != nil {
				t.Fatal(err)
			}
			if got, err := load(base, fixedNames, owner()); err != nil || got != (Switches{}) {
				t.Fatalf("%s %v: absent file refused: %v %v", parent, mode, got, err)
			}
			if got, err := load(base, fixedNames, owner()+1); err != nil || got != (Switches{}) {
				t.Fatalf("%s %v: absent file under a foreign owner refused: %v %v", parent, mode, got, err)
			}
			if mode == 0700 {
				continue
			}
			if err := os.WriteFile(file, []byte(enabled), 0644); err != nil {
				t.Fatal(err)
			}
			refused(t, base, owner(), "present file under writable "+parent)
		}
	}
}

// interleaved loads the file with hook run at one race point, and returns the error.
func interleaved(t *testing.T, at string, hook func(file string)) error {
	t.Helper()
	base, file := fixture(t, enabled)
	interleave = func(point string) {
		if point == at {
			hook(file)
		}
	}
	defer func() { interleave = func(string) {} }()
	got, err := load(base, fixedNames, owner())
	if got != (Switches{}) {
		t.Fatalf("accepted %v", got)
	}
	return err
}

// A same-size change after the read (here a mode change that is still trusted)
// is caught on the descriptor that was read, before the name is rechecked.
func TestLoadRefusesAFileChangedWhileItWasRead(t *testing.T) {
	err := interleaved(t, "read", func(file string) {
		if err := os.Chmod(file, 0600); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(err, ErrActivation) || !strings.Contains(err.Error(), "file changed while it was read") {
		t.Fatal(err)
	}
}

// A trusted, valid file renamed over the name after the read is refused: the
// bytes read are not what the name now holds.
func TestLoadRefusesAFileReplacedWhileItWasRead(t *testing.T) {
	err := interleaved(t, "checked", func(file string) {
		other := file + ".new"
		if err := os.WriteFile(other, []byte(enabled), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(other, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(other, file); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(err, ErrActivation) || !strings.Contains(err.Error(), "file was replaced while it was read") {
		t.Fatal(err)
	}
}

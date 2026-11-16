//go:build linux

package registeredinspection

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func inputCheckout(t *testing.T) (string, *os.File, uint64) {
	t.Helper()
	if os.Getuid() == 0 || os.Getgid() == 0 {
		t.Fatal("ordinary input tests require a non-root UID/GID")
	}
	path := t.TempDir()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return path, f, uint64(info.Sys().(*syscall.Stat_t).Dev)
}
func observeInputCheckout(f *os.File, dev uint64) (*InputObservation, error) {
	return readInputs(f, "web", uint32(os.Getuid()), uint32(os.Getgid()), dev)
}
func TestObserveInputsRetainsExactBytesAndDetectsChanges(t *testing.T) {
	path, f, dev := inputCheckout(t)
	if err := os.Mkdir(filepath.Join(path, ".bench"), 0700); err != nil {
		t.Fatal(err)
	}
	config := []byte("[setup]\nonce = 'echo ready'\n")
	if err := os.WriteFile(filepath.Join(path, ".bench/config.toml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "package.json"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	o, err := observeInputCheckout(f, dev)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	v := o.Snapshot()
	if !bytes.Equal(v.Config.Data, config) || !v.Manifests[0].Present || v.Manifests[0].Bytes != 0 || v.Manifests[1].Present {
		t.Fatalf("incorrect observed inputs: %+v", v)
	}
	v.Config.Data[0] = 'x'
	if !bytes.Equal(o.Snapshot().Config.Data, config) {
		t.Fatal("snapshot aliases retained config")
	}
	if err := o.Check(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "package-lock.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := o.Check(); !errors.Is(err, ErrBoundary) {
		t.Fatalf("new formerly absent input accepted: %v", err)
	}
}
func TestObserveInputsRejectsSpecialAndOversizedFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "hardlink", "oversize", "config-symlink"} {
		t.Run(kind, func(t *testing.T) {
			path, f, dev := inputCheckout(t)
			target := filepath.Join(path, "package.json")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink("missing", target)
			case "fifo":
				err = syscall.Mkfifo(target, 0600)
			case "hardlink":
				original := filepath.Join(path, "original")
				err = os.WriteFile(original, []byte("{}"), 0600)
				if err == nil {
					err = os.Link(original, target)
				}
			case "oversize":
				var out *os.File
				out, err = os.OpenFile(target, os.O_CREATE|os.O_RDWR, 0600)
				if err == nil {
					err = out.Truncate(MaxDependencyBytes + 1)
					_ = out.Close()
				}
			case "config-symlink":
				err = os.Symlink("elsewhere", filepath.Join(path, ".bench"))
			}
			if err != nil {
				t.Fatal(err)
			}
			o, err := observeInputCheckout(f, dev)
			if o != nil {
				o.Close()
			}
			if !errors.Is(err, ErrBoundary) || o != nil {
				t.Fatalf("%s accepted: %v", kind, err)
			}
		})
	}
}
func TestObserveInputsRejectsReplacementAndClosedObservation(t *testing.T) {
	path, f, dev := inputCheckout(t)
	target := filepath.Join(path, "package.json")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	o, err := observeInputCheckout(f, dev)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if err := os.Rename(target, target+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := o.Check(); !errors.Is(err, ErrBoundary) {
		t.Fatalf("byte-identical inode replacement accepted: %v", err)
	}
	o.Close()
	if err := o.Check(); !errors.Is(err, ErrBoundary) {
		t.Fatalf("closed observation accepted: %v", err)
	}
}

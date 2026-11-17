//go:build linux

package registeredinspection

import (
	"bytes"
	"errors"
	"fmt"
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

func TestObserveInputsRefusesForeignDeviceAndWritableConfigDirectory(t *testing.T) {
	for _, kind := range []string{"file-device", "directory-device", "group-writable", "other-writable"} {
		t.Run(kind, func(t *testing.T) {
			path, f, dev := inputCheckout(t)
			if kind == "file-device" {
				if err := os.WriteFile(filepath.Join(path, "package.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				configDir := filepath.Join(path, ".bench")
				if err := os.Mkdir(configDir, 0700); err != nil {
					t.Fatal(err)
				}
				mode := os.FileMode(0700)
				if kind == "group-writable" {
					mode = 0720
				}
				if kind == "other-writable" {
					mode = 0702
				}
				if err := os.Chmod(configDir, mode); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "file-device" || kind == "directory-device" {
				dev ^= 1
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

func TestObserveInputsEnforcesReadSideConfigCap(t *testing.T) {
	for _, size := range []int{MaxConfigBytes, MaxConfigBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path, f, dev := inputCheckout(t)
			if err := os.Mkdir(filepath.Join(path, ".bench"), 0700); err != nil {
				t.Fatal(err)
			}
			data := bytes.Repeat([]byte("x"), size)
			if err := os.WriteFile(filepath.Join(path, ".bench/config.toml"), data, 0600); err != nil {
				t.Fatal(err)
			}
			o, err := observeInputCheckout(f, dev)
			if o != nil {
				defer o.Close()
			}
			if size > MaxConfigBytes {
				if !errors.Is(err, ErrBoundary) || o != nil {
					t.Fatalf("oversized config accepted: %v", err)
				}
			} else if err != nil || o == nil || !bytes.Equal(o.Snapshot().Config.Data, data) {
				t.Fatalf("config at exact limit refused or changed: %v", err)
			}
		})
	}
}

func TestInputCheckRefusesConfigDirectoryReplacementEditAndAddition(t *testing.T) {
	for _, kind := range []string{"directory-replaced", "config-edited-in-place", "config-added"} {
		t.Run(kind, func(t *testing.T) {
			path, f, dev := inputCheckout(t)
			dir, target := filepath.Join(path, ".bench"), filepath.Join(path, ".bench/config.toml")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if kind != "config-added" {
				if err := os.WriteFile(target, []byte("before"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			o, err := observeInputCheckout(f, dev)
			if err != nil {
				t.Fatal(err)
			}
			defer o.Close()
			if err = o.Check(); err != nil {
				t.Fatal("valid baseline refused", err)
			}
			switch kind {
			case "directory-replaced":
				if err = os.Rename(dir, dir+".original"); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			case "config-edited-in-place":
				before, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(target, []byte("after with different size"), 0600); err != nil {
					t.Fatal(err)
				}
				after, err := os.Stat(target)
				if err != nil || !os.SameFile(before, after) {
					t.Fatal("edit replaced inode", err)
				}
			case "config-added":
				if err = os.WriteFile(target, []byte("new config"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err = o.Check(); !errors.Is(err, ErrBoundary) {
				t.Fatalf("%s accepted: %v", kind, err)
			}
		})
	}
}

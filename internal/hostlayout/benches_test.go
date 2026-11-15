package hostlayout

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestManagedBenchParentRemainsTraversableWithServiceUmask(t *testing.T) {
	if root := os.Getenv("SUPERBENCH_PARENT_077_ROOT"); root != "" {
		// Isolate the umask in this one-purpose subprocess, never a shared test or
		// helper process. The shipped service runs with this restrictive umask.
		syscall.Umask(0077)
		if err := PrepareBenches(root); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(root, "benches"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0755 {
			t.Fatalf("fixed managed parent is %04o; service owner cannot inspect assigned children", info.Mode().Perm())
		}
		return
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestManagedBenchParentRemainsTraversableWithServiceUmask$", "-test.v")
	cmd.Env = append(os.Environ(), "SUPERBENCH_PARENT_077_ROOT="+root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated service-umask regression: %v\n%s", err, output)
	}
}

func TestManagedBenchParentNeverWidensExistingOrUnknownPaths(t *testing.T) {
	for _, kind := range []string{"private", "writable", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "benches")
			mode := os.FileMode(0700)
			switch kind {
			case "file":
				err = os.WriteFile(path, []byte("preserve"), 0600)
			case "symlink":
				outside := filepath.Join(root, "outside")
				if err = os.Mkdir(outside, 0700); err == nil {
					err = os.Symlink(outside, path)
				}
			default:
				if kind == "writable" {
					mode = 0775
				}
				if err = os.Mkdir(path, 0700); err == nil {
					err = os.Chmod(path, mode)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			err = PrepareBenches(root)
			if (kind == "private") != (err == nil) {
				t.Fatalf("unexpected refusal %s: %v", kind, err)
			}
			after, e := os.Lstat(path)
			if e != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("existing path changed", e)
			}
			if kind == "file" {
				raw, e := os.ReadFile(path)
				if e != nil || string(raw) != "preserve" {
					t.Fatal("file bytes changed")
				}
			}
		})
	}
}

func TestManagedBenchParentRefusesReplacementBeforeModeChange(t *testing.T) {
	for _, replace := range []string{"child", "root", "symlink"} {
		t.Run(replace, func(t *testing.T) {
			outer, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(outer, "data")
			if err = os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			child := filepath.Join(root, "benches")
			if err = os.Mkdir(child, 0700); err != nil {
				t.Fatal(err)
			}
			r, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			c, err := os.Open(child)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ri, _ := r.Stat()
			ci, _ := c.Stat()
			p := benchParent{root: r, child: c, rootName: root, childName: child, rootID: ri, childID: ci}
			retained := filepath.Join(root, "retained")
			if replace == "root" {
				retained = filepath.Join(outer, "retained")
				err = os.Rename(root, retained)
				if err == nil {
					err = os.Mkdir(root, 0750)
				}
				if err == nil {
					err = os.Mkdir(child, 0750)
				}
			} else {
				err = os.Rename(child, retained)
				if err == nil && replace == "symlink" {
					err = os.Symlink(retained, child)
				} else if err == nil {
					err = os.Mkdir(child, 0750)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(child)
			if err != nil {
				t.Fatal(err)
			}
			if err = p.finishNew(); err == nil {
				t.Fatal("accepted replaced fixed parent")
			}
			after, err := os.Lstat(child)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("replacement changed", err)
			}
			old, err := c.Stat()
			if err != nil || old.Mode().Perm() != 0700 {
				t.Fatal("unpublished retained inode changed", err)
			}
		})
	}
}

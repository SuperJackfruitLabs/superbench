//go:build linux

package gitcontrol

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Available probes an explicitly configured delegated cgroup-v2 root. It does
// not create a scope, adopt a host root or establish tenant isolation.
func Available(root string) bool { return validateRoot(root) == nil && syscall.Access(root, 3) == nil }
func validateRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("Git control root must be canonical and absolute")
	}
	actual, e := filepath.EvalSymlinks(root)
	if e != nil || actual != root {
		return errors.New("Git control root must be a physical cgroup directory")
	}
	info, e := os.Lstat(root)
	if e != nil || !info.IsDir() {
		return errors.New("Git control root is not a directory")
	}
	var stat syscall.Statfs_t
	if e = syscall.Statfs(root, &stat); e != nil || stat.Type != 0x63677270 {
		return errors.New("Git control root is not cgroup v2")
	}
	if _, e = os.Stat(filepath.Join(root, "cgroup.controllers")); e != nil {
		return errors.New("Git control root lacks cgroup-v2 controls")
	}
	return nil
}

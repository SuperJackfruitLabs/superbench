// Package storagecustody contains fixed-root physical path and bounded file checks.
// It does not authorize first installation or repair storage metadata.
package storagecustody

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// RootOwnedDirectory preserves fixed-root directory custody checks.
func RootOwnedDirectory(path string, private bool) error { return rootOwnedDirectory(path, private, 0) }

// TrustedAncestors permits root-owned sticky ancestors, never writable nonsticky ones.
func TrustedAncestors(path string) error { return trustedAncestors(path, 0) }

func rootOwnedDirectory(path string, private bool, owner uint32) error {
	if e := trustedAncestors(path, owner); e != nil {
		return e
	}
	stat, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 || stat.Sys().(*syscall.Stat_t).Uid != owner || stat.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("%s must be a root-owned directory without group/world write", path)
	}
	actual, e := filepath.EvalSymlinks(path)
	if e != nil {
		return e
	}
	if actual != path {
		return errors.New("quota policy directory traverses a symlink")
	}
	if private && stat.Mode().Perm()&0077 != 0 {
		return errors.New("privileged ledger directory must be private")
	}
	return nil
}

func trustedAncestors(path string, owner uint32) error {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		stat, e := os.Lstat(dir)
		if e != nil {
			return e
		}
		if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 || stat.Sys().(*syscall.Stat_t).Uid != 0 && stat.Sys().(*syscall.Stat_t).Uid != owner {
			return errors.New("privileged path has an untrusted ancestor")
		}
		if stat.Mode().Perm()&0022 != 0 && stat.Mode()&os.ModeSticky == 0 {
			return errors.New("privileged path has a writable non-sticky ancestor")
		}
		if dir == "/" {
			return nil
		}
	}
}

// PhysicalRoot observes an existing physical root without imposing an owner.
func PhysicalRoot(root string) (uint64, uint64, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return 0, 0, errors.New("storage root must be an existing physical absolute directory")
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, 0, err
	}
	if physical != root {
		return 0, 0, errors.New("storage root contains a symlink")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return 0, 0, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return 0, 0, errors.New("storage root is not a directory")
	}
	st := info.Sys().(*syscall.Stat_t)
	return uint64(st.Dev), st.Ino, nil
}

// Package storagecustody preserves fixed-root policy filesystem custody checks.
package storagecustody

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// TrustedAncestors permits root-owned sticky ancestors, never writable nonsticky ones.
func TrustedAncestors(path string) error {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		stat, e := os.Lstat(dir)
		if e != nil {
			return e
		}
		if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 || stat.Sys().(*syscall.Stat_t).Uid != 0 {
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

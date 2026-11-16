//go:build linux

package payloadexec

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

// Each component is opened relative to its already-verified parent. No caller
// pathname can redirect the resulting descriptor through a symlink. Root-owned
// sticky ancestors protect their root/daemon-owned children; other writable
// ancestors and owners are rejected.
func openPhysicalDirectory(path string, daemonUID uint32) (*os.File, error) {
	// Policy.Validate independently refuses / as a configured root. This
	// internal reader also needs / as the fixed parent of /bin.
	if path != "/" && !directory(path) {
		return nil, errors.New("invalid physical directory")
	}
	parent, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	check := func(file *os.File) error {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		owner := info.Sys().(*syscall.Stat_t).Uid
		if !info.IsDir() || owner != 0 && owner != daemonUID || info.Mode().Perm()&0022 != 0 && !(owner == 0 && info.Mode()&os.ModeSticky != 0) {
			return errors.New("physical directory has untrusted ownership or permissions")
		}
		return nil
	}
	if err := check(parent); err != nil {
		_ = parent.Close()
		return nil, err
	}
	if path == "/" {
		return parent, nil
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		fd, err := syscall.Openat(int(parent.Fd()), component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		_ = parent.Close()
		if err != nil {
			return nil, err
		}
		parent = os.NewFile(uintptr(fd), component)
		if err := check(parent); err != nil {
			_ = parent.Close()
			return nil, err
		}
	}
	return parent, nil
}

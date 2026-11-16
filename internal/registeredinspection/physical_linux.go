//go:build linux

package registeredinspection

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"os"
	"strings"
	"syscall"
)

// Each component is opened relative to its already-verified parent. No caller
// pathname can redirect the resulting descriptor through a symlink. Root-owned
// sticky ancestors protect their root/daemon-owned children; other writable
// ancestors and owners are rejected.
func OpenPhysicalDirectory(path string, daemonUID uint32) (*os.File, error) {
	// Policy.Validate independently refuses / as a configured root. This
	// internal reader also needs / as the fixed parent of /bin.
	if path != "/" && !payloadpolicy.Directory(path) {
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

func PrivateDirectory(parent *os.File, name string, p Policy, expected Node) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return nil, ErrBoundary
	}
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.Join(ErrBoundary, err)
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, errors.Join(ErrBoundary, err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || st.Uid != p.PayloadUID || st.Gid != p.PayloadGID || info.Mode().Perm()&0027 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || uint64(st.Dev) != expected.Device || expected.Inode != 0 && st.Ino != expected.Inode {
		_ = file.Close()
		return nil, ErrBoundary
	}
	return file, nil
}

func MatchesNode(file *os.File, expected Node) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	st := info.Sys().(*syscall.Stat_t)
	return uint64(st.Dev) == expected.Device && st.Ino == expected.Inode
}

func SamePolicyFile(a, b os.FileInfo) bool {
	left, right := a.Sys().(*syscall.Stat_t), b.Sys().(*syscall.Stat_t)
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && left.Uid == right.Uid && left.Gid == right.Gid && left.Nlink == right.Nlink && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

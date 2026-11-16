package quotaoperationjournal

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func boundedEntries(path string, limit int) ([]os.DirEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(limit + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > limit {
		return nil, ErrAuthority
	}
	return entries, nil
}

// A private leaf is insufficient when another UID can rename an ancestor.
// Root-owned sticky temporary ancestors are safe for an owned private child;
// other shared-writable ancestors or foreign owners are refused.
func ancestry(path string, owner uint32) (func() error, error) {
	var pins []pinned
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		i, e := os.Lstat(p)
		if e != nil {
			return nil, e
		}
		st, ok := i.Sys().(*syscall.Stat_t)
		if !ok || !i.IsDir() || (st.Uid != 0 && st.Uid != owner) || i.Mode().Perm()&0022 != 0 && (st.Uid != 0 || i.Mode()&os.ModeSticky == 0) {
			return nil, ErrAuthority
		}
		pins = append(pins, pinned{path: p, info: i, directory: true})
		if p == "/" {
			break
		}
	}
	return func() error {
		for _, p := range pins {
			now, e := os.Lstat(p.path)
			if e != nil || !os.SameFile(p.info, now) || now.Mode() != p.info.Mode() {
				return ErrAuthority
			}
			a, ok := now.Sys().(*syscall.Stat_t)
			b := p.info.Sys().(*syscall.Stat_t)
			if !ok || a.Uid != b.Uid {
				return ErrAuthority
			}
		}
		return nil
	}, nil
}

type pinned struct {
	path      string
	info      os.FileInfo
	directory bool
}

func private(info os.FileInfo, owner uint32, dir bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != owner {
		return false
	}
	if dir {
		return info.IsDir() && info.Mode().Perm() == 0700
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && st.Nlink == 1
}
func directory(path string, owner uint32) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, ErrAuthority
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if physical != path {
		return nil, ErrAuthority
	}
	i, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !private(i, owner, true) {
		return nil, ErrAuthority
	}
	return i, nil
}
func openFile(path string, owner uint32, create bool) (*os.File, error) {
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	i, err := f.Stat()
	if err != nil || !private(i, owner, false) {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrAuthority
	}
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(i, named) {
		f.Close()
		return nil, ErrAuthority
	}
	return f, nil
}
func syncDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func lock(ctx context.Context, f *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			return nil
		}
		if e != syscall.EWOULDBLOCK && e != syscall.EAGAIN {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

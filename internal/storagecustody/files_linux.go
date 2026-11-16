//go:build linux

package storagecustody

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// OpenProtected preserves the legacy reader: regular root-owned file, no
// group/world write, NOFOLLOW and NONBLOCK. It does not check ancestor or nlink.
func OpenProtected(path string) (*os.File, error) { return openProtected(path, 0) }

// ReadProtected preserves the legacy max+1 bounded read, with no repair effects.
func ReadProtected(path string, max int64) ([]byte, error) { return readProtected(path, max, 0) }

func openProtected(path string, owner uint32) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Uid != owner {
		f.Close()
		return nil, errors.New("storage authority must be a protected root-owned regular file")
	}
	return f, nil
}

func readProtected(path string, max int64, owner uint32) ([]byte, error) {
	f, err := openProtected(path, owner)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, errors.New("storage authority exceeds size limit")
	}
	return raw, nil
}

package quotahelper

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

const maxBinary = 64 << 20

func binaryHash(f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBinary {
		return "", errors.New("helper executable exceeds bounds")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxBinary+1))
	if err != nil {
		return "", err
	}
	end, err := f.Stat()
	if err != nil {
		return "", err
	}
	if n != info.Size() || n > maxBinary || info.Size() != end.Size() || !info.ModTime().Equal(end.ModTime()) || info.Mode() != end.Mode() {
		return "", errors.New("helper executable changed")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SelfBinarySHA256 hashes the kernel's actual running image, not a possibly
// replaced pathname. This is used only by the managed Linux fixed helper.
func SelfBinarySHA256() (string, error) {
	if runtime.GOOS != "linux" {
		return "", errors.New("managed helper image identity requires Linux")
	}
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		return "", err
	}
	defer f.Close()
	return binaryHash(f)
}
func installedBinary(path string, uid uint32) (string, error) {
	actual, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if actual != path {
		return "", errors.New("fixed helper path must be physical")
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		st := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || (st.Uid != 0 && st.Uid != uid) || info.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return "", errors.New("fixed helper has writable or foreign ancestors")
		}
		if dir == "/" {
			break
		}
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Uid != uid || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 {
		return "", errors.New("fixed helper must be a protected owned executable")
	}
	hash, err := binaryHash(f)
	if err != nil {
		return "", err
	}
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, named) {
		return "", errors.New("fixed helper pathname changed")
	}
	return hash, nil
}

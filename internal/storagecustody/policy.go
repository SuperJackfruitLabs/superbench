package storagecustody

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// LoadPolicy preserves the fixed policy reader, including its legacy single-decode behavior.
func LoadPolicy(path string, dst any) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("policy path must be absolute and normalized")
	}
	if e := TrustedAncestors(path); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return e
	}
	defer f.Close()
	stat, e := f.Stat()
	if e != nil {
		return e
	}
	actual, e := filepath.EvalSymlinks(path)
	if e != nil {
		return e
	}
	absolute, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	if actual != absolute {
		return errors.New("policy symlinks are not allowed")
	}
	if !stat.Mode().IsRegular() || stat.Mode().Perm()&0022 != 0 || stat.Sys().(*syscall.Stat_t).Uid != 0 {
		return errors.New("policy must be a root-owned regular file, not group/world writable")
	}
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if e = dec.Decode(dst); e != nil {
		return e
	}
	return nil
}

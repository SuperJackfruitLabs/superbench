package mirrorstage

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

func deleteEntry(stage *boundDir, expected reap.Entry, parents map[string]reap.Entry) error {
	parent := stage
	var opened []*boundDir
	defer func() {
		for i := len(opened) - 1; i >= 0; i-- {
			opened[i].close()
		}
	}()
	relative := filepath.Dir(expected.Path)
	if relative != "." {
		prefix := ""
		for _, part := range strings.Split(relative, string(filepath.Separator)) {
			prefix = filepath.Join(prefix, part)
			allowed, ok := parents[prefix]
			if !ok || !os.FileMode(allowed.Mode).IsDir() {
				return errors.New("unapproved entry parent")
			}
			child, e := parent.child(part)
			if e != nil {
				return e
			}
			opened = append(opened, child)
			if child.id != (Identity{Device: allowed.Device, Inode: allowed.Inode, Mode: allowed.Mode}) {
				return errors.New("entry ancestor changed")
			}
			parent = child
		}
	}
	actual, e := inspectEntry(parent, filepath.Base(expected.Path), expected.Path)
	if e != nil {
		return e
	}
	if actual != expected {
		return errors.New("entry changed during collection; preserve it")
	}
	if e = checkDirs(append([]*boundDir{stage}, opened...)...); e != nil {
		return e
	}
	if e = parent.root.Remove(filepath.Base(expected.Path)); e != nil {
		return e
	}
	return parent.sync()
}
func inspectEntry(parent *boundDir, name, path string) (reap.Entry, error) {
	var v reap.Entry
	info, e := parent.root.Lstat(name)
	if e != nil {
		return v, e
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return v, errors.New("filesystem entry identity unavailable")
	}
	v = reap.Entry{Path: path, Device: uint64(st.Dev), Inode: st.Ino, Mode: uint32(info.Mode())}
	h := sha256.New()
	g1, g256 := sha1.New(), sha256.New()
	switch {
	case info.IsDir():
	case info.Mode().IsRegular():
		f, e := parent.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return v, e
		}
		opened, e := f.Stat()
		if e != nil {
			f.Close()
			return v, e
		}
		if !os.SameFile(info, opened) {
			f.Close()
			return v, errors.New("entry replaced before reading")
		}
		prefix := fmt.Sprintf("blob %d\x00", info.Size())
		g1.Write([]byte(prefix))
		g256.Write([]byte(prefix))
		_, readErr := io.Copy(io.MultiWriter(h, g1, g256), f)
		after, statErr := f.Stat()
		closeErr := f.Close()
		if e = errors.Join(readErr, statErr, closeErr); e != nil {
			return v, e
		}
		if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || after.Mode() != info.Mode() {
			return v, errors.New("entry changed while reading")
		}
	case info.Mode().Type() == os.ModeSymlink:
		target, e := parent.root.Readlink(name)
		if e != nil {
			return v, e
		}
		prefix := fmt.Sprintf("blob %d\x00", len(target))
		g1.Write([]byte(prefix))
		g256.Write([]byte(prefix))
		io.WriteString(io.MultiWriter(h, g1, g256), target)
	default:
		return v, errors.New("special filesystem entry cannot be collected")
	}
	named, e := parent.root.Lstat(name)
	if e != nil {
		return v, e
	}
	if !os.SameFile(info, named) || info.Mode() != named.Mode() {
		return v, errors.New("entry replaced while reading")
	}
	v.SHA256 = hex.EncodeToString(h.Sum(nil))
	if !info.IsDir() {
		v.GitSHA1 = hex.EncodeToString(g1.Sum(nil))
		v.GitSHA256 = hex.EncodeToString(g256.Sum(nil))
	}
	return v, parent.check()
}

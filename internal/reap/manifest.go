// Package reap snapshots approved directory contents without following symlinks.
package reap

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"unicode/utf8"
)

const MaxManifest = 16 << 20
const MaxEntries = 100000

type Entry struct {
	Path        string
	Mode        uint32
	Device      uint64
	Inode       uint64
	SHA256      string
	GitSHA1     string
	GitSHA256   string
	Recoverable bool
}
type Tree struct {
	Root    Entry
	Entries []Entry
}

func inspect(root, relative string, recoverable bool) (Entry, error) {
	p := filepath.Join(root, relative)
	st, e := os.Lstat(p)
	if e != nil {
		return Entry{}, e
	}
	s := st.Sys().(*syscall.Stat_t)
	v := Entry{Path: relative, Mode: uint32(st.Mode()), Device: uint64(s.Dev), Inode: s.Ino, Recoverable: recoverable}
	hash := sha256.New()
	object1, object256 := sha1.New(), sha256.New()
	objectSize := st.Size()
	if st.Mode()&os.ModeSymlink != 0 {
		target, e := os.Readlink(p)
		if e != nil {
			return v, e
		}
		objectSize = int64(len(target))
	}
	prefix := fmt.Sprintf("blob %d\x00", objectSize)
	_, _ = object1.Write([]byte(prefix))
	_, _ = object256.Write([]byte(prefix))
	writer := io.MultiWriter(hash, object1, object256)
	switch {
	case st.Mode().IsRegular():
		f, e := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if e != nil {
			return v, e
		}
		opened, e := f.Stat()
		if e != nil {
			f.Close()
			return v, e
		}
		if !os.SameFile(st, opened) {
			f.Close()
			return v, errors.New("file changed during snapshot")
		}
		_, e = io.Copy(writer, f)
		closeErr := f.Close()
		if e != nil {
			return v, e
		}
		if closeErr != nil {
			return v, closeErr
		}
	case st.Mode()&os.ModeSymlink != 0:
		target, e := os.Readlink(p)
		if e != nil {
			return v, e
		}
		_, _ = writer.Write([]byte(target))
	case st.IsDir():
	default:
		return v, errors.New("special filesystem entries cannot be reaped")
	}
	v.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if !st.IsDir() {
		v.GitSHA1 = hex.EncodeToString(object1.Sum(nil))
		v.GitSHA256 = hex.EncodeToString(object256.Sum(nil))
	}
	return v, nil
}
func Snapshot(root string, tracked map[string]bool) (Tree, error) {
	var tree Tree
	if e := rejectMounts(root); e != nil {
		return tree, e
	}
	if !utf8.ValidString(root) {
		return tree, errors.New("invalid UTF-8 root path")
	}
	entry, e := inspect(root, ".", false)
	if e != nil {
		return tree, e
	}
	if os.FileMode(entry.Mode)&os.ModeSymlink != 0 || !os.FileMode(entry.Mode).IsDir() {
		return tree, errors.New("reap root must be a directory")
	}
	tree.Root = entry
	e = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if !utf8.ValidString(rel) {
			return errors.New("invalid UTF-8 entry path")
		}
		if len(tree.Entries) >= MaxEntries {
			return errors.New("reap entry limit exceeded")
		}
		v, e := inspect(root, rel, tracked[rel])
		if e != nil {
			return e
		}
		if v.Device != tree.Root.Device {
			return errors.New("nested filesystem refused")
		}
		tree.Entries = append(tree.Entries, v)
		return nil
	})
	if e != nil {
		return tree, e
	}
	raw, e := json.Marshal(tree)
	if e != nil {
		return tree, e
	}
	if len(raw) > MaxManifest {
		return tree, errors.New("reap manifest exceeds limit")
	}
	return tree, nil
}

// Remaining permits only unchanged entries from an already durable deletion
// intent. Missing entries are the only accepted evidence of partial progress.
func Remaining(root string, approved Tree) (Tree, error) {
	tracked := map[string]bool{}
	allowed := map[string]Entry{}
	for _, v := range approved.Entries {
		allowed[v.Path] = v
		tracked[v.Path] = v.Recoverable
	}
	actual, e := Snapshot(root, tracked)
	if e != nil {
		return actual, e
	}
	if actual.Root != approved.Root {
		return actual, errors.New("reap root identity changed")
	}
	for _, v := range actual.Entries {
		if prior, ok := allowed[v.Path]; !ok || prior != v {
			return actual, errors.New("directory changed since approval; preserve remaining files")
		}
	}
	return actual, nil
}
func DeleteContents(root string, approved Tree) error {
	actual, e := Remaining(root, approved)
	if e != nil {
		return e
	}
	for i := len(actual.Entries) - 1; i >= 0; i-- {
		v := actual.Entries[i]
		now, e := inspect(root, v.Path, v.Recoverable)
		if e != nil {
			return e
		}
		if now != v {
			return errors.New("entry changed during deletion; preserve remaining files")
		}
		if e = os.Remove(filepath.Join(root, v.Path)); e != nil {
			return e
		}
	}
	return nil
}

package reap

import (
	"errors"
	"os"
	"syscall"
)

func readOnly(e Entry) Entry {
	if os.FileMode(e.Mode)&os.ModeSymlink == 0 {
		e.Mode &^= 0222
	}
	return e
}

// permissionFile pins the expected physical entry before changing its mode.
// A multiply-linked regular file could alias another checkout, so refuse it.
func permissionFile(root *os.Root, expected Entry) (*os.File, error) {
	f, e := root.OpenFile(expected.Path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	id := st.Sys().(*syscall.Stat_t)
	if uint64(id.Dev) != expected.Device || id.Ino != expected.Inode || uint32(st.Mode()) != expected.Mode || st.Mode().IsRegular() && id.Nlink != 1 {
		f.Close()
		return nil, errors.New("archive permission target changed or has multiple hard links")
	}
	return f, nil
}

func setMode(root *os.Root, expected Entry, mode uint32) error {
	f, e := permissionFile(root, expected)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = f.Chmod(os.FileMode(mode)); e != nil {
		return e
	}
	return f.Sync()
}

// freezeTree requires every original entry, allowing only the exact intended
// write-bit removal as evidence of interrupted progress. Symlinks are untouched.
func freezeTree(root string, original Tree) (Tree, error) {
	actual, e := Snapshot(root, nil)
	if e != nil {
		return actual, e
	}
	if actual.Root != original.Root && actual.Root != readOnly(original.Root) || len(actual.Entries) != len(original.Entries) {
		return actual, errors.New("archive tree changed during permission transition")
	}
	for i, v := range actual.Entries {
		prior := original.Entries[i]
		if v != prior && v != readOnly(prior) {
			return actual, errors.New("archive entry changed during permission transition")
		}
	}
	return actual, nil
}

func permissionPreflight(root *os.Root, entries []Entry) error {
	for _, v := range entries {
		if os.FileMode(v.Mode)&os.ModeSymlink != 0 {
			continue
		}
		f, e := permissionFile(root, v)
		if e != nil {
			return e
		}
		if e = f.Close(); e != nil {
			return e
		}
	}
	return nil
}

// FreezePreflight validates every entry before publishing an archive intent.
func FreezePreflight(path string, tree Tree) error {
	actual, e := freezeTree(path, tree)
	if e != nil {
		return e
	}
	root, e := os.OpenRoot(path)
	if e != nil {
		return e
	}
	defer root.Close()
	return permissionPreflight(root, append([]Entry{actual.Root}, actual.Entries...))
}

// Freeze applies only a previously journaled transition. It never removes data,
// changes symlink targets or adopts changed files to make a retry succeed.
func Freeze(path string, original Tree) error { return freeze(path, original, nil) }

func freeze(path string, original Tree, after func() error) error {
	actual, e := freezeTree(path, original)
	if e != nil {
		return e
	}
	root, e := os.OpenRoot(path)
	if e != nil {
		return e
	}
	defer root.Close()
	entries := append([]Entry{actual.Root}, actual.Entries...)
	if e = permissionPreflight(root, entries); e != nil {
		return e
	}
	for i := len(entries) - 1; i >= 0; i-- {
		v := entries[i]
		if os.FileMode(v.Mode)&os.ModeSymlink != 0 || readOnly(v) == v {
			continue
		}
		if e = setMode(root, v, readOnly(v).Mode); e != nil {
			return e
		}
		if after != nil {
			if e = after(); e != nil {
				return e
			}
		}
	}
	_, e = Frozen(path, original)
	return e
}

// Frozen requires all original entries with their final read-only modes.
func Frozen(path string, original Tree) (Tree, error) {
	actual, e := Snapshot(path, nil)
	if e != nil {
		return actual, e
	}
	if actual.Root != readOnly(original.Root) || len(actual.Entries) != len(original.Entries) {
		return actual, errors.New("archive is not the complete read-only tree")
	}
	for i, v := range actual.Entries {
		if v != readOnly(original.Entries[i]) {
			return actual, errors.New("archive is not the complete read-only tree")
		}
	}
	return actual, nil
}

// DeleteFrozenContents is called only after a durable reviewed deletion intent.
// During retry, directories may have gained only owner-write permission; files
// must still match exactly. No new entries are allowed by Remaining.
func DeleteFrozenContents(path string, approved Tree) error {
	tracked := map[string]bool{}
	for _, v := range approved.Entries {
		tracked[v.Path] = v.Recoverable
	}
	actual, e := Snapshot(path, tracked)
	if e != nil {
		return e
	}
	allowed := map[string]Entry{".": approved.Root}
	for _, v := range approved.Entries {
		allowed[v.Path] = v
	}
	entries := append([]Entry{actual.Root}, actual.Entries...)
	for _, v := range entries {
		prior, ok := allowed[v.Path]
		if !ok || readOnly(prior) != prior {
			return errors.New("invalid frozen reap approval")
		}
		advanced := prior
		if os.FileMode(prior.Mode).IsDir() {
			advanced.Mode |= 0200
		}
		if v != prior && v != advanced {
			return errors.New("frozen archive changed after deletion approval")
		}
	}
	root, e := os.OpenRoot(path)
	if e != nil {
		return e
	}
	defer root.Close()
	for _, v := range entries {
		if os.FileMode(v.Mode).IsDir() && v.Mode&0200 == 0 {
			if e = setMode(root, v, v.Mode|0200); e != nil {
				return e
			}
		}
	}
	working := Tree{Root: approved.Root, Entries: append([]Entry(nil), approved.Entries...)}
	working.Root.Mode |= 0200
	for i := range working.Entries {
		if os.FileMode(working.Entries[i].Mode).IsDir() {
			working.Entries[i].Mode |= 0200
		}
	}
	return DeleteContents(path, working)
}

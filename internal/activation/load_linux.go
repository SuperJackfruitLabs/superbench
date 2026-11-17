//go:build linux

package activation

import (
	"errors"
	"io"
	"os"
	"syscall"
)

const (
	directoryFlags = syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	leafFlags      = syscall.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
)

var fixedNames = []string{"etc", "superbench", "activation.json"}

// Load reads FixedPath. An absent file, or an absent /etc/superbench, yields the
// all-false defaults whatever its directories look like. A present file that
// fails any check refuses startup.
func Load() (Switches, error) { return load("/", fixedNames, 0) }

type leafIdentity struct {
	Device, Inode, Links uint64
	Mode, UID, GID       uint32
	Size                 int64
	Modified, Changed    syscall.Timespec
}

// interleave runs at the two points where a concurrent writer could change the
// file ("read") or rebind its name ("checked"); only tests replace it.
var interleave = func(point string) {}

// Every ancestor from the base is held open while the name is resolved, and
// symlinks are never followed. Only a present file needs trusted ancestors: then
// each held directory must be owned by the owner (root in production) without
// group or world write, like the installed payload policy and service registry.
// The checks read the held descriptors, so they cover the directories actually
// walked, not a later binding of their names.
func load(base string, names []string, owner uint32) (Switches, error) {
	fd, err := syscall.Open(base, directoryFlags, 0)
	if err != nil {
		return Switches{}, refuse("open " + base + ": " + err.Error())
	}
	held := []*os.File{os.NewFile(uintptr(fd), base)}
	defer func() {
		for _, f := range held {
			_ = f.Close()
		}
	}()
	for _, name := range names[:len(names)-1] {
		fd, err = syscall.Openat(int(held[len(held)-1].Fd()), name, directoryFlags, 0)
		if errors.Is(err, syscall.ENOENT) {
			return Switches{}, nil
		}
		if err != nil {
			return Switches{}, refuse("open directory " + name + ": " + err.Error())
		}
		held = append(held, os.NewFile(uintptr(fd), name))
	}
	parent, name := held[len(held)-1], names[len(names)-1]
	fd, err = syscall.Openat(int(parent.Fd()), name, leafFlags, 0)
	if errors.Is(err, syscall.ENOENT) {
		return Switches{}, nil
	}
	if err != nil {
		return Switches{}, refuse("open " + name + ": " + err.Error())
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	for _, directory := range held {
		if err = trustedDirectory(directory, owner); err != nil {
			return Switches{}, err
		}
	}
	before, err := trustedLeaf(file, owner)
	if err != nil {
		return Switches{}, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxSize+1))
	if err != nil {
		return Switches{}, refuse("read: " + err.Error())
	}
	interleave("read")
	after, err := trustedLeaf(file, owner)
	if err != nil || after != before || int64(len(raw)) != before.Size {
		return Switches{}, refuse("file changed while it was read")
	}
	interleave("checked")
	// The name must still bind to the file that was read.
	fd, err = syscall.Openat(int(parent.Fd()), name, leafFlags, 0)
	if err != nil {
		return Switches{}, refuse("file was replaced while it was read")
	}
	named := os.NewFile(uintptr(fd), name)
	namedID, err := trustedLeaf(named, owner)
	_ = named.Close()
	if err != nil || namedID != before {
		return Switches{}, refuse("file was replaced while it was read")
	}
	return Parse(raw)
}

func trustedDirectory(f *os.File, owner uint32) error {
	var s syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &s); err != nil {
		return refuse("stat " + f.Name() + ": " + err.Error())
	}
	if !directoryOK(&s, owner) {
		return refuse(f.Name() + " must be a root-owned directory without group or world write")
	}
	return nil
}

func directoryOK(s *syscall.Stat_t, owner uint32) bool {
	return s.Mode&syscall.S_IFMT == syscall.S_IFDIR && s.Uid == owner && s.Mode&0022 == 0
}

func trustedLeaf(f *os.File, owner uint32) (leafIdentity, error) {
	var s syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &s); err != nil {
		return leafIdentity{}, refuse("stat: " + err.Error())
	}
	if !leafOK(&s, owner) {
		return leafIdentity{}, refuse("must be a single-link root-owned regular file without group or world write, at most 4 KiB")
	}
	return leafIdentity{uint64(s.Dev), s.Ino, uint64(s.Nlink), s.Mode, s.Uid, s.Gid, s.Size, s.Mtim, s.Ctim}, nil
}

func leafOK(s *syscall.Stat_t, owner uint32) bool {
	return s.Mode&syscall.S_IFMT == syscall.S_IFREG && s.Uid == owner && s.Mode&0022 == 0 && s.Nlink == 1 && s.Size >= 1 && s.Size <= MaxSize
}

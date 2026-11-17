//go:build linux

package quotaleaf

import (
	"errors"
	"io"
	"os"
	"syscall"
	"unsafe"
)

const (
	fsIocFsgetxattr = 0x801c581f // _IOR('X', 31, struct fsxattr)
	oPath           = 0x200000
	maxDepth        = 128
)

// getattr is Get; unprivileged tests substitute observed attributes.
var getattr = Get

// Get reads the attributes through an open descriptor, never by path.
func Get(fd int) (Attr, error) {
	var raw struct {
		Xflags, Extsize, Nextents, Projid, Cowextsize uint32
		Pad                                           [8]byte
	}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), fsIocFsgetxattr, uintptr(unsafe.Pointer(&raw))); e != 0 {
		return Attr{}, e
	}
	return Attr{Xflags: raw.Xflags, ProjectID: raw.Projid}, nil
}

// Leaf checks benches/key without following a symlink. An absent leaf holds no
// data and passes; the callers decide whether absence is allowed.
func Leaf(benches, key string, project uint32) error {
	parent, err := syscall.Open(benches, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer syscall.Close(parent)
	fd, err := syscall.Openat(parent, key, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	a, err := getattr(fd)
	if err != nil {
		return err
	}
	if !Check(a, project, true) {
		return ErrLeafProject
	}
	return nil
}

// Tree checks the leaf and every directory and regular file beneath it, as
// root, through descriptors only. It never follows a symlink, never leaves the
// leaf's filesystem and refuses rather than continue past limit entries.
// Symlinks and special files are counted, not checked: an ioctl needs a real
// descriptor, which root never opens on them.
func Tree(leaf *os.File, project uint32, limit int) error {
	var root syscall.Stat_t
	if err := syscall.Fstat(int(leaf.Fd()), &root); err != nil {
		return err
	}
	a, err := getattr(int(leaf.Fd()))
	if err != nil {
		return err
	}
	if root.Mode&syscall.S_IFMT != syscall.S_IFDIR || !Check(a, project, true) {
		return ErrLeafProject
	}
	w := walker{device: root.Dev, project: project, left: limit}
	return w.dir(int(leaf.Fd()), 0)
}

type walker struct {
	device  uint64
	project uint32
	left    int
}

func (w *walker) dir(fd, depth int) error {
	if depth >= maxDepth {
		return ErrTreeLimit
	}
	dup, err := syscall.Dup(fd)
	if err != nil {
		return err
	}
	syscall.CloseOnExec(dup)
	d := os.NewFile(uintptr(dup), "bench directory")
	defer d.Close()
	if _, err = d.Seek(0, io.SeekStart); err != nil {
		return err
	}
	for {
		names, err := d.Readdirnames(256)
		for _, name := range names {
			if w.left--; w.left < 0 {
				return ErrTreeLimit
			}
			if err := w.entry(fd, name, depth); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (w *walker) entry(parent int, name string, depth int) error {
	path, err := syscall.Openat(parent, name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	var st syscall.Stat_t
	err = syscall.Fstat(path, &st)
	syscall.Close(path)
	if err != nil {
		return err
	}
	kind := st.Mode & syscall.S_IFMT
	if kind != syscall.S_IFDIR && kind != syscall.S_IFREG {
		return nil
	}
	if st.Dev != w.device {
		return ErrTreeProject
	}
	flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_NOCTTY | syscall.O_CLOEXEC
	if kind == syscall.S_IFDIR {
		flags |= syscall.O_DIRECTORY
	}
	fd, err := syscall.Openat(parent, name, flags, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var again syscall.Stat_t
	if err = syscall.Fstat(fd, &again); err != nil {
		return err
	}
	if again.Dev != st.Dev || again.Ino != st.Ino {
		return ErrTreeProject // replaced between the two opens
	}
	a, err := getattr(fd)
	if err != nil {
		return err
	}
	if !Check(a, w.project, kind == syscall.S_IFDIR) {
		return ErrTreeProject
	}
	if kind == syscall.S_IFDIR {
		return w.dir(fd, depth+1)
	}
	return nil
}

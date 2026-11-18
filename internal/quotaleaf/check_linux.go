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
	fsIocFsgetxattr   = 0x801c581f // _IOR('X', 31, struct fsxattr)
	oPath             = 0x200000
	sysFileGetattr    = 468 // the same number on amd64 and arm64
	atSymlinkNofollow = 0x100
)

// getattr is Get and getattrAt is GetAt; unprivileged tests substitute
// observed attributes.
var getattr, getattrAt = Get, GetAt

// errNoGetattrAt reports a kernel or filesystem without file_getattr.
var errNoGetattrAt = errors.New("file_getattr unavailable")

// GetAt reads a symlink's or special file's attributes by name below dir
// with file_getattr (468, Linux 6.17), never following or opening it. Older
// kernels and filesystems without it report errNoGetattrAt.
func GetAt(dir int, name string) (Attr, error) {
	path, err := syscall.BytePtrFromString(name)
	if err != nil {
		return Attr{}, err
	}
	var raw struct {
		Xflags                                uint64
		Extsize, Nextents, Projid, Cowextsize uint32
	}
	_, _, e := syscall.Syscall6(sysFileGetattr, uintptr(dir), uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&raw)), unsafe.Sizeof(raw), atSymlinkNofollow, 0)
	switch e {
	case 0:
		return Attr{Xflags: uint32(raw.Xflags), ProjectID: raw.Projid}, nil
	case syscall.ENOSYS, syscall.EOPNOTSUPP, syscall.ENOTTY, syscall.EINVAL:
		return Attr{}, errNoGetattrAt
	}
	return Attr{}, e
}

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
// Symlinks and special files are never opened: their project is read by name
// with file_getattr where the kernel has it (6.17 and later), and on older
// kernels they are only counted, which is a gap. Depth is bounded only by limit:
// it holds one directory descriptor at a time, returning to a parent through
// ".." and proving that it is the node it left.
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
	cur, err := syscall.Dup(int(leaf.Fd()))
	if err != nil {
		return err
	}
	syscall.CloseOnExec(cur)
	w := walker{device: root.Dev, project: project, left: limit, cur: cur}
	defer func() { syscall.Close(w.cur) }()
	return w.walk(root)
}

// level is one directory on the walk: its node and its unvisited names.
type level struct {
	dev, ino uint64
	names    []string
}

type walker struct {
	device  uint64
	project uint32
	left    int
	cur     int
	stack   []level
}

func (w *walker) walk(root syscall.Stat_t) error {
	names, err := w.names()
	if err != nil {
		return err
	}
	w.stack = []level{{root.Dev, root.Ino, names}}
	for len(w.stack) > 0 {
		top := &w.stack[len(w.stack)-1]
		if len(top.names) == 0 {
			w.stack = w.stack[:len(w.stack)-1]
			if len(w.stack) > 0 {
				if err = w.up(w.stack[len(w.stack)-1]); err != nil {
					return err
				}
			}
			continue
		}
		name := top.names[0]
		top.names = top.names[1:]
		if err = w.entry(name); err != nil {
			return err
		}
	}
	return nil
}

// names reads every name of the current directory, counting them against limit.
func (w *walker) names() ([]string, error) {
	dup, err := syscall.Dup(w.cur)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(dup)
	d := os.NewFile(uintptr(dup), "bench directory")
	defer d.Close()
	if _, err = d.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var all []string
	for {
		names, err := d.Readdirnames(256)
		if w.left -= len(names); w.left < 0 {
			return nil, ErrTreeLimit
		}
		all = append(all, names...)
		if err == io.EOF {
			return all, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// up returns to the parent through ".." and requires it to be want's node.
func (w *walker) up(want level) error {
	fd, err := syscall.Openat(w.cur, "..", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	var st syscall.Stat_t
	if err = syscall.Fstat(fd, &st); err != nil || st.Dev != want.dev || st.Ino != want.ino {
		syscall.Close(fd)
		return errors.Join(ErrTreeProject, err) // moved during the walk
	}
	syscall.Close(w.cur)
	w.cur = fd
	return nil
}

func (w *walker) entry(name string) error {
	path, err := syscall.Openat(w.cur, name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
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
		// Never opened: read by name where the kernel can, else only counted.
		a, err := getattrAt(w.cur, name)
		if errors.Is(err, errNoGetattrAt) {
			return nil
		}
		if err != nil {
			return err
		}
		if !Check(a, w.project, false) {
			return ErrTreeProject
		}
		return nil
	}
	if st.Dev != w.device {
		return ErrTreeProject
	}
	flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_NOCTTY | syscall.O_CLOEXEC
	if kind == syscall.S_IFDIR {
		flags |= syscall.O_DIRECTORY
	}
	fd, err := syscall.Openat(w.cur, name, flags, 0)
	if err != nil {
		return err
	}
	var again syscall.Stat_t
	if err = syscall.Fstat(fd, &again); err == nil && (again.Dev != st.Dev || again.Ino != st.Ino) {
		err = ErrTreeProject // replaced between the two opens
	}
	var a Attr
	if err == nil {
		a, err = getattr(fd)
	}
	if err == nil && !Check(a, w.project, kind == syscall.S_IFDIR) {
		err = ErrTreeProject
	}
	if err != nil || kind != syscall.S_IFDIR {
		syscall.Close(fd)
		return err
	}
	syscall.Close(w.cur)
	w.cur = fd
	names, err := w.names()
	if err != nil {
		return err
	}
	w.stack = append(w.stack, level{st.Dev, st.Ino, names})
	return nil
}

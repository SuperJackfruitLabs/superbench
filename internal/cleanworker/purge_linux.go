//go:build linux

package cleanworker

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fsIocFsgetxattr is _IOR('X', 31, struct fsxattr) on amd64 and arm64.
const fsIocFsgetxattr = uintptr(2<<30 | 28<<16 | 'X'<<8 | 31)

// fsxattr mirrors the kernel's struct fsxattr.
type fsxattr struct {
	Xflags, Extsize, Nextents, Projid, Cowextsize uint32
	Pad                                           [8]byte
}

var errChanged = fmt.Errorf("%w: an entry changed during the purge; retry the same purge", ErrWorker)

// purger empties one tree that its own account owns. Every access is relative
// to a held directory descriptor and no path below the root is resolved, so a
// symlink, hard link, FIFO, socket or device is unlinked by its name and never
// opened or followed. A directory is entered only once the descriptor opened
// for it is proved to be the node its name showed, on the root's device, owned
// by this account and carrying the tree's project.
type purger struct {
	root         int
	device       uint64
	uid, project uint32
	steps, limit int
	lifted       uint64
	projectOf    func(fd int) (uint32, error)
	// beforeEnter runs between a directory's lstat and its open; tests only.
	beforeEnter func(name string)
}

// purge empties the tree at root, which must be target's node, and leaves the
// root itself, emptied and fsynced, for root's own removal. A refusal leaves
// every entry it has not yet removed; the same purge then continues.
func purge(root string, target Target) error {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return errors.Join(ErrWorker, err)
	}
	if err = mountsBeneath(raw, root); err != nil {
		return err
	}
	p := &purger{uid: uint32(os.Getuid()), project: target.ProjectID, limit: purgeSteps, projectOf: projectID}
	return p.run(root, target)
}

func (p *purger) run(root string, target Target) error {
	var named unix.Stat_t
	if err := unix.Lstat(root, &named); err != nil {
		return err
	}
	if named.Dev != target.Device || named.Ino != target.Inode {
		return fmt.Errorf("%w: the tree's root is not the approved node", ErrWorker)
	}
	p.device = target.Device
	fd, err := p.enter(unix.AT_FDCWD, root, named)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	p.root = fd
	if err = p.empty(fd, 0); err != nil {
		return err
	}
	// The unlinks are committed through the ordered XFS log with the root.
	return unix.Fsync(fd)
}

// enter opens the directory name below parent that lstat showed as seen. It
// pins the node with O_PATH first, so a directory its owner made unreadable is
// given owner-only access through that exact node, never through the name.
func (p *purger) enter(parent int, name string, seen unix.Stat_t) (int, error) {
	if seen.Mode&unix.S_IFMT != unix.S_IFDIR || seen.Dev != p.device {
		return -1, errChanged
	}
	if p.beforeEnter != nil {
		p.beforeEnter(name)
	}
	pinned, err := unix.Openat(parent, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
		return -1, errChanged
	}
	if err != nil {
		return -1, err
	}
	defer unix.Close(pinned)
	var st unix.Stat_t
	if err = unix.Fstat(pinned, &st); err != nil {
		return -1, err
	}
	if st.Dev != seen.Dev || st.Ino != seen.Ino || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return -1, errChanged
	}
	if st.Uid != p.uid {
		return -1, fmt.Errorf("%w: a directory the purging account does not own", ErrWorker)
	}
	if st.Mode&0o7777 != 0o700 {
		if err = unix.Chmod("/proc/self/fd/"+strconv.Itoa(pinned), 0o700); err != nil {
			return -1, err
		}
	}
	fd, err := unix.Openat(pinned, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	var held unix.Stat_t
	if err = unix.Fstat(fd, &held); err != nil || held.Dev != st.Dev || held.Ino != st.Ino {
		unix.Close(fd)
		return -1, errors.Join(errChanged, err)
	}
	project, err := p.projectOf(fd)
	if err != nil || project != p.project {
		unix.Close(fd)
		return -1, errors.Join(fmt.Errorf("%w: a directory outside the tree's project", ErrWorker), err)
	}
	return fd, nil
}

// empty removes every entry of the held directory at depth, rereading it
// until a pass finds nothing: a directory lifted to the root arrives there
// while the root is being read.
func (p *purger) empty(dir, depth int) error {
	buf := make([]byte, 16<<10)
	for {
		if _, err := unix.Seek(dir, 0, 0); err != nil {
			return err
		}
		found := false
		for {
			n, err := unix.Getdents(dir, buf)
			if err != nil {
				return err
			}
			if n <= 0 {
				break
			}
			_, _, names := unix.ParseDirent(buf[:n], -1, nil)
			for _, name := range names {
				found = true
				if err = p.remove(dir, name, depth); err != nil {
					return err
				}
			}
		}
		if !found {
			return nil
		}
	}
}

// remove deletes one name below the held directory at depth.
func (p *purger) remove(dir int, name string, depth int) error {
	if p.steps++; p.steps > p.limit {
		return fmt.Errorf("%w: purge step bound reached; retry the same purge", ErrWorker)
	}
	var st unix.Stat_t
	err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Dev != p.device {
		return fmt.Errorf("%w: another filesystem beneath the tree", ErrWorker)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		// Only this name goes: a hard link's inode keeps its other names.
		if err = unix.Unlinkat(dir, name, 0); errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if depth+1 >= purgeDepth {
		return p.lift(dir, name)
	}
	child, err := p.enter(dir, name, st)
	if err != nil {
		return err
	}
	err = p.empty(child, depth+1)
	unix.Close(child)
	if err != nil {
		return err
	}
	if err = unix.Unlinkat(dir, name, unix.AT_REMOVEDIR); errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

// lift renames a directory found at the depth bound to a fresh name beneath
// the root, without replacing anything there, where a later pass empties it.
func (p *purger) lift(dir int, name string) error {
	for range 1000 {
		p.lifted++
		err := unix.Renameat2(dir, name, p.root, liftName(p.lifted), unix.RENAME_NOREPLACE)
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return err
		}
	}
	return fmt.Errorf("%w: no free name beneath the root for a deep directory", ErrWorker)
}

// projectID reads the XFS project ID through the held descriptor.
func projectID(fd int) (uint32, error) {
	var a fsxattr
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), fsIocFsgetxattr, uintptr(unsafe.Pointer(&a))); e != 0 {
		return 0, e
	}
	return a.Projid, nil
}

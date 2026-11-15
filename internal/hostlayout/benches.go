package hostlayout

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

type benchParent struct {
	root, child         *os.File
	rootName, childName string
	rootID, childID     os.FileInfo
}

func (p benchParent) check() error {
	for _, entry := range []struct {
		name     string
		identity os.FileInfo
	}{{p.rootName, p.rootID}, {p.childName, p.childID}} {
		named, err := os.Lstat(entry.name)
		if err != nil {
			return err
		}
		if !named.IsDir() || named.Mode()&os.ModeSymlink != 0 || !os.SameFile(named, entry.identity) || named.Mode() != entry.identity.Mode() || named.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || named.Mode().Perm()&0022 != 0 {
			return errors.New("fixed managed parent identity changed or is untrusted")
		}
	}
	return nil
}

func (p benchParent) finishNew() error {
	if err := p.check(); err != nil {
		return err
	}
	// Only the descriptor for the just-created verified inode may be widened.
	// Never change the process umask or chmod an existing pathname.
	if err := p.child.Chmod(0755); err != nil {
		return err
	}
	var err error
	if p.childID, err = p.child.Stat(); err != nil {
		return err
	}
	if err = p.child.Sync(); err != nil {
		return err
	}
	if err = p.root.Sync(); err != nil {
		return err
	}
	return p.check()
}

// PrepareBenches creates only the fixed root-owned traversal parent. The
// privileged caller has already validated its fixed policy root. Existing
// directories retain their exact permissions; task/shared targets remain 0700.
func PrepareBenches(root string) error {
	actual, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if actual != root || !filepath.IsAbs(root) {
		return errors.New("fixed managed parent requires a physical root")
	}
	rootFD, err := os.OpenFile(root, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer rootFD.Close()
	rootID, err := rootFD.Stat()
	if err != nil {
		return err
	}
	path := filepath.Join(root, "benches")
	created := true
	if err = os.Mkdir(path, 0755); err != nil {
		if !os.IsExist(err) {
			return err
		}
		created = false
	}
	initial, err := os.Lstat(path)
	if err != nil {
		return err
	}
	child, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer child.Close()
	childID, err := child.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(initial, childID) {
		return errors.New("fixed managed parent changed before open")
	}
	p := benchParent{root: rootFD, child: child, rootName: root, childName: path, rootID: rootID, childID: childID}
	if created {
		return p.finishNew()
	}
	return p.check()
}

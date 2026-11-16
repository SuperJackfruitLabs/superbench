//go:build linux

package storagecustody

import (
	"errors"
	"os"
	"syscall"
)

type OperationRoot struct {
	Device, Inode  uint64
	Mode, UID, GID uint32
}

func OperationRootInfo(path string, private bool) (OperationRoot, error) {
	var zero OperationRoot
	if err := RootOwnedDirectory(path, private); err != nil {
		return zero, err
	}
	device, inode, err := PhysicalRoot(path)
	if err != nil {
		return zero, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return zero, err
	}
	st := info.Sys().(*syscall.Stat_t)
	if device != uint64(st.Dev) || inode != st.Ino {
		return zero, errors.New("operation storage identity changed")
	}
	return OperationRoot{device, inode, uint32(info.Mode()), st.Uid, st.Gid}, nil
}

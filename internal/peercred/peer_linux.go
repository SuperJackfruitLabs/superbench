//go:build linux

package peercred

import "syscall"

func UID(fd int) (uint32, error) {
	c, e := syscall.GetsockoptUcred(fd, syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	if e != nil {
		return 0, e
	}
	return c.Uid, nil
}

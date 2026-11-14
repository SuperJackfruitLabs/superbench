//go:build linux

package service

import "syscall"

func peerUID(fd int) (uint32, error) {
	c, e := syscall.GetsockoptUcred(fd, syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	if e != nil {
		return 0, e
	}
	return c.Uid, nil
}

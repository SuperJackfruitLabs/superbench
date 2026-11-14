//go:build darwin

package service

import "golang.org/x/sys/unix"

func peerUID(fd int) (uint32, error) {
	c, e := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if e != nil {
		return 0, e
	}
	return c.Uid, nil
}

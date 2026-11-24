//go:build linux

// Package payloadsession holds bench-payload's session-mode leaves: the stdio
// and descriptor checks and the bench's private mount namespace. It runs only
// in the fixed helper, as root, before the credential drop.
package payloadsession

import (
	"errors"
	"syscall"
)

var ErrSession = errors.New("managed payload session refused")

// VerifyStdio: fd 0 is the daemon's control socket (an AF_UNIX stream socket
// whose peer is the daemon account); fd 1 and fd 2 are daemon-owned write-only
// FIFOs for the refusal code. Anything else refuses before a frame is read.
func VerifyStdio(daemonUID uint32) error { return stdio(0, 1, 2, daemonUID) }

// Stdin selects session mode: fd 0 is a socket. Frame kinds never mix.
func Stdin() bool {
	var st syscall.Stat_t
	return syscall.Fstat(0, &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFSOCK
}

func stdio(in, out, errOut int, daemonUID uint32) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(in, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFSOCK {
		return errors.Join(ErrSession, err)
	}
	domain, err := syscall.GetsockoptInt(in, syscall.SOL_SOCKET, 39) // SO_DOMAIN
	if err != nil || domain != syscall.AF_UNIX {
		return errors.Join(ErrSession, err)
	}
	kind, err := syscall.GetsockoptInt(in, syscall.SOL_SOCKET, syscall.SO_TYPE)
	if err != nil || kind != syscall.SOCK_STREAM {
		return errors.Join(ErrSession, err)
	}
	peer, err := syscall.GetsockoptUcred(in, syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	if err != nil || peer.Uid != daemonUID {
		return errors.Join(ErrSession, err)
	}
	for _, fd := range []int{out, errOut} {
		if err := fifo(fd, syscall.O_WRONLY, daemonUID); err != nil {
			return err
		}
	}
	return nil
}

// fifo requires a pipe with exactly this access mode, owned by owner.
func fifo(fd int, access uintptr, owner uint32) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return errors.Join(ErrSession, err)
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
	if errno != 0 || st.Mode&syscall.S_IFMT != syscall.S_IFIFO || st.Uid != owner || flags&syscall.O_ACCMODE != access {
		return errors.Join(ErrSession, errno)
	}
	return nil
}

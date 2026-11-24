//go:build linux

package remotessh

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	ptySupported = true
	tiocgptpeer  = 0x5441 // TIOCGPTPEER: the slave without a path lookup
)

// openPTY opens a fresh pseudo-terminal pair. The slave comes from the
// master by TIOCGPTPEER, never by a /dev/pts path, so it cannot be another
// terminal. The master is non-blocking for the runtime poller; the slave is
// blocking and goes to the launcher. hold is a second slave descriptor the
// server keeps until the session's output is drained: with no slave open,
// Linux can report EIO on the master before the last output is read.
func openPTY() (master, slave, hold *os.File, err error) {
	master, slave, err = openPair()
	if err != nil {
		return nil, nil, nil, err
	}
	h, _, errno := syscall.Syscall(syscall.SYS_FCNTL, slave.Fd(), syscall.F_DUPFD_CLOEXEC, 0)
	if errno != 0 {
		_ = master.Close()
		_ = slave.Close()
		return nil, nil, nil, errno
	}
	return master, slave, os.NewFile(h, "pty slave hold"), nil
}

func openPair() (master, slave *os.File, err error) {
	m, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(m), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		_ = syscall.Close(m)
		return nil, nil, errno
	}
	s, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(m), tiocgptpeer, uintptr(syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC))
	if errno != 0 {
		_ = syscall.Close(m)
		return nil, nil, errno
	}
	if err := syscall.SetNonblock(m, true); err != nil {
		_ = syscall.Close(m)
		_ = syscall.Close(int(s))
		return nil, nil, err
	}
	return os.NewFile(uintptr(m), "pty master"), os.NewFile(s, "pty slave"), nil
}

// setWinsize sets the terminal size on the master (TIOCSWINSZ).
func setWinsize(master *os.File, cols, rows uint32) error {
	ws := struct{ Row, Col, X, Y uint16 }{Row: uint16(rows), Col: uint16(cols)}
	raw, err := master.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := raw.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

//go:build linux

package payloadsession

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// Receive takes the session's descriptors from the control socket: exactly one
// data byte 'F' carrying exactly want descriptors, then end of stream. Every
// descriptor arrives close-on-exec. On any refusal every descriptor the kernel
// installed, including truncated or trailing ones, is closed.
func Receive(control int, want int, timeout time.Duration) (_ []*os.File, err error) {
	if want != 1 && want != 3 || timeout <= 0 || timeout > 30*time.Second {
		return nil, ErrSession
	}
	kind, err := syscall.GetsockoptInt(control, syscall.SOL_SOCKET, syscall.SO_TYPE)
	if err != nil || kind != syscall.SOCK_STREAM {
		return nil, errors.Join(ErrSession, err)
	}
	// A blocking receive bounded by SO_RCVTIMEO; never a busy loop.
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(control), syscall.F_GETFL, 0)
	if errno == 0 {
		_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, uintptr(control), syscall.F_SETFL, flags&^syscall.O_NONBLOCK)
	}
	if errno != 0 {
		return nil, errors.Join(ErrSession, errno)
	}
	deadline := time.Now().Add(timeout)
	var received []*os.File
	defer func() {
		if err != nil {
			for _, f := range received {
				_ = f.Close()
			}
		}
	}()
	first, n, err := receive(control, want, deadline)
	received = append(received, first...)
	if err != nil {
		return nil, err
	}
	if n != 1 || len(first) != want {
		return nil, ErrSession
	}
	extra, n, err := receive(control, want, deadline)
	received = append(received, extra...)
	if err != nil || n != 0 || len(extra) != 0 {
		return nil, errors.Join(ErrSession, err)
	}
	return first, nil
}

// receive returns every descriptor the kernel installed, even with an error.
// The control buffer holds want descriptors; more is MSG_CTRUNC.
func receive(control, want int, deadline time.Time) ([]*os.File, int, error) {
	var data [1]byte
	oob := make([]byte, syscall.CmsgSpace(want*4))
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, 0, ErrSession
		}
		tv := syscall.NsecToTimeval(remaining.Nanoseconds())
		if tv.Sec == 0 && tv.Usec == 0 {
			tv.Usec = 1
		}
		if err := syscall.SetsockoptTimeval(control, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
			return nil, 0, errors.Join(ErrSession, err)
		}
		n, oobn, flags, _, err := syscall.Recvmsg(control, data[:], oob, syscall.MSG_CMSG_CLOEXEC)
		if err == syscall.EINTR {
			continue
		}
		files, parseErr := rights(oob[:max(oobn, 0)])
		if err != nil || parseErr != nil || flags&(syscall.MSG_CTRUNC|syscall.MSG_TRUNC) != 0 || n == 1 && data[0] != 'F' {
			return files, n, errors.Join(ErrSession, err, parseErr)
		}
		return files, n, nil
	}
}

// rights wraps every SCM_RIGHTS descriptor; any other control message refuses.
func rights(oob []byte) ([]*os.File, error) {
	messages, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, errors.Join(ErrSession, err)
	}
	var files []*os.File
	for _, m := range messages {
		fds, parseErr := syscall.ParseUnixRights(&m)
		for _, fd := range fds {
			files = append(files, os.NewFile(uintptr(fd), "session descriptor"))
		}
		if parseErr != nil || m.Header.Level != syscall.SOL_SOCKET || m.Header.Type != syscall.SCM_RIGHTS {
			err = ErrSession
		}
	}
	return files, err
}

// VerifyDescriptors: a PTY session gets one daemon-owned read-write PTY slave
// (UNIX98 majors 136-143); otherwise stdin, stdout and stderr are daemon-owned
// pipes with exactly the read, write and write access modes.
func VerifyDescriptors(fds []*os.File, pty bool, daemonUID uint32) error {
	for _, f := range fds {
		if f == nil {
			return ErrSession
		}
	}
	if pty {
		if len(fds) != 1 {
			return ErrSession
		}
		var st syscall.Stat_t
		if err := syscall.Fstat(int(fds[0].Fd()), &st); err != nil {
			return errors.Join(ErrSession, err)
		}
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fds[0].Fd(), syscall.F_GETFL, 0)
		major := uint64(st.Rdev)>>8&0xfff | uint64(st.Rdev)>>32&^0xfff
		if errno != 0 || st.Mode&syscall.S_IFMT != syscall.S_IFCHR || major < 136 || major > 143 || st.Uid != daemonUID || flags&syscall.O_ACCMODE != syscall.O_RDWR {
			return errors.Join(ErrSession, errno)
		}
		return nil
	}
	if len(fds) != 3 {
		return ErrSession
	}
	for i, access := range []uintptr{syscall.O_RDONLY, syscall.O_WRONLY, syscall.O_WRONLY} {
		if err := fifo(int(fds[i].Fd()), access, daemonUID); err != nil {
			return err
		}
	}
	return nil
}

// Prepare readies the session's descriptors for the PID 1 stage: a PTY slave
// becomes the payload user's, mode 0600, and every descriptor is blocking.
func Prepare(fds []*os.File, pty bool, uid, gid uint32) error {
	if len(fds) != 1 && len(fds) != 3 || pty != (len(fds) == 1) {
		return ErrSession
	}
	if pty {
		if err := fds[0].Chown(int(uid), int(gid)); err != nil {
			return errors.Join(ErrSession, err)
		}
		if err := fds[0].Chmod(0600); err != nil {
			return errors.Join(ErrSession, err)
		}
	}
	for _, f := range fds {
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETFL, 0)
		if errno == 0 {
			_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_SETFL, flags&^syscall.O_NONBLOCK)
		}
		if errno != 0 {
			return errors.Join(ErrSession, errno)
		}
	}
	return nil
}

//go:build linux

package payloadexec

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"os"
	"runtime"
	"strconv"
	"syscall"
)

var ErrBoundary = registeredinspection.ErrBoundary

func verifyStdio(p Policy) error {
	for fd := 0; fd <= 2; fd++ {
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			return errors.Join(ErrBoundary, err)
		}
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
		access := flags & syscall.O_ACCMODE
		want := uintptr(syscall.O_WRONLY)
		if fd == 0 {
			want = syscall.O_RDONLY
		}
		if errno != 0 || st.Mode&syscall.S_IFMT != syscall.S_IFIFO || st.Uid != p.DaemonUID || access != want {
			return errors.Join(ErrBoundary, errno)
		}
	}
	return nil
}

// Mark, then verify, every non-stdio descriptor close-on-exec. This preserves
// pinned authority handles for the final checks but gives the executed payload
// no database, control socket, private log reader or other inherited capability.
// The shipped Linux architectures share close_range syscall number 436; old
// kernels and unsupported architectures refuse instead of using a racy fallback.
func descriptorsCloseOnExec() error {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return ErrBoundary
	}
	const closeRange = 436
	const closeRangeCloexec = 1 << 2
	if _, _, errno := syscall.Syscall(closeRange, 3, ^uintptr(0), closeRangeCloexec); errno != 0 {
		return errors.Join(ErrBoundary, errno)
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return errors.Join(ErrBoundary, err)
	}
	for _, entry := range entries {
		fd, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil {
			return errors.Join(ErrBoundary, err)
		}
		if fd <= 2 {
			continue
		}
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
		if errno == syscall.EBADF {
			continue // os.ReadDir's own descriptor has already closed.
		}
		if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
			return errors.Join(ErrBoundary, errno)
		}
	}
	return nil
}

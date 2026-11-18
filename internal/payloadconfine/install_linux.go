//go:build linux

package payloadconfine

import (
	"errors"
	"strings"
	"syscall"
	"unsafe"
)

// ErrConfine reports that the filter is not, or not provably, installed.
var ErrConfine = errors.New("payload confinement refused")

const (
	prGetNoNewPrivs         = 39
	seccompSetModeFilter    = 1
	seccompFilterFlagTsync  = 1
	statusSeccompFilterMode = "2"
)

type sockFprog struct {
	Len    uint16
	_      [6]byte
	Filter *Instruction
}

// Install applies the filter to every thread of the calling process. It
// requires no_new_privs, which the caller must already have set, and proves
// filter mode afterwards. It never falls back to an unfiltered launch.
func Install() error {
	arch, nr, ok := native()
	if !ok {
		return ErrConfine
	}
	set, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prGetNoNewPrivs, 0, 0)
	if errno != 0 || set != 1 {
		return ErrConfine
	}
	prog := Program(arch)
	fprog := sockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	r, _, errno := syscall.Syscall(nr, seccompSetModeFilter, seccompFilterFlagTsync, uintptr(unsafe.Pointer(&fprog)))
	if errno != 0 || r != 0 {
		// With TSYNC a positive result names a thread that could not sync.
		return errors.Join(ErrConfine, errno)
	}
	return Verify()
}

// Verify requires the kernel to report seccomp filter mode for this process.
// It reads /proc/self/status with raw system calls, never an os.File, so a
// caller that later requires an exact descriptor table finds no poller.
func Verify() error {
	fd, err := syscall.Open("/proc/self/status", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return errors.Join(ErrConfine, err)
	}
	defer syscall.Close(fd)
	raw := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := syscall.Read(fd, buf)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return errors.Join(ErrConfine, err)
		}
		if n == 0 {
			break
		}
		if raw = append(raw, buf[:n]...); len(raw) > 64<<10 {
			return ErrConfine
		}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if value, found := strings.CutPrefix(line, "Seccomp:"); found {
			if strings.TrimSpace(value) == statusSeccompFilterMode {
				return nil
			}
			return ErrConfine
		}
	}
	return ErrConfine
}

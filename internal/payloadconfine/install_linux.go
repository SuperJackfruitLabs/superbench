//go:build linux

package payloadconfine

import (
	"errors"
	"os"
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
func Verify() error {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil || len(raw) > 64<<10 {
		return errors.Join(ErrConfine, err)
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

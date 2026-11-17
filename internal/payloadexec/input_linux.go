//go:build linux

package payloadexec

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"
)

const inputSeals = 1 | 2 | 4 | 8 // SEAL, SHRINK, GROW, WRITE
const addSeals = 1033
const getSeals = 1034

// openPayloadInput turns already-bounded opaque bytes into anonymous immutable
// storage. No filesystem path, pipe writer, goroutine or daemon FD survives exec.
// Like close_range, the syscall is supported only on the shipped Linux arches.
func openPayloadInput(r Request) (*os.File, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if len(r.Input) == 0 {
		return nil, nil
	}
	var number uintptr
	switch runtime.GOARCH {
	case "amd64":
		number = 319
	case "arm64":
		number = 279
	default:
		return nil, ErrFrame
	}
	name, err := syscall.BytePtrFromString("superbench-payload-input")
	if err != nil {
		return nil, errors.Join(ErrFrame, err)
	}
	fd, _, errno := syscall.Syscall(number, uintptr(unsafe.Pointer(name)), 3, 0) // CLOEXEC | ALLOW_SEALING
	runtime.KeepAlive(name)
	if errno != 0 {
		return nil, errors.Join(ErrFrame, errno)
	}
	writable := os.NewFile(fd, "finite payload input")
	defer writable.Close()
	if err := writable.Chmod(0600); err != nil {
		return nil, errors.Join(ErrFrame, err)
	}
	n, err := writable.Write(r.Input)
	if err != nil || n != len(r.Input) {
		return nil, errors.Join(ErrFrame, err)
	}
	if _, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, addSeals, inputSeals); errno != 0 {
		return nil, errors.Join(ErrFrame, errno)
	}
	// Open this owned descriptor read-only, never a caller-selected pathname.
	readonly, err := os.Open("/proc/self/fd/" + strconv.Itoa(int(fd)))
	if err != nil {
		return nil, errors.Join(ErrFrame, err)
	}
	before, a := writable.Stat()
	after, b := readonly.Stat()
	if a != nil || b != nil || !os.SameFile(before, after) || after.Size() != int64(len(r.Input)) {
		readonly.Close()
		return nil, errors.Join(ErrFrame, a, b)
	}
	if err := checkPayloadInput(readonly); err != nil {
		readonly.Close()
		return nil, err
	}
	return readonly, nil
}
func checkPayloadInput(f *os.File) error {
	if f == nil {
		return ErrFrame
	}
	s, err := f.Stat()
	if err != nil || !s.Mode().IsRegular() || s.Size() <= 0 || s.Size() > MaxPayloadInput {
		return errors.Join(ErrFrame, err)
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETFL, 0)
	if errno != 0 || flags&syscall.O_ACCMODE != syscall.O_RDONLY {
		return errors.Join(ErrFrame, errno)
	}
	seals, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), getSeals, 0)
	if errno != 0 || seals != inputSeals {
		return errors.Join(ErrFrame, errno)
	}
	offset, err := f.Seek(0, 1)
	if err != nil || offset != 0 {
		return errors.Join(ErrFrame, err)
	}
	return nil
}

// Called only after all privileged authority/stdio checks and credential drop.
// The replacement has no capability beyond reading the exact sealed task bytes.
func bindPayloadInput(f *os.File) error {
	if err := checkPayloadInput(f); err != nil {
		return err
	}
	// Dup3 rather than Dup2: linux/arm64 has no dup2. Dup3 refuses old == new, where Dup2 was a no-op.
	if fd := int(f.Fd()); fd != 0 {
		if err := syscall.Dup3(fd, 0, 0); err != nil {
			return errors.Join(ErrFrame, err)
		}
	}
	a, err := f.Stat()
	b, other := os.Stdin.Stat()
	if err != nil || other != nil || !os.SameFile(a, b) {
		return errors.Join(ErrFrame, err, other)
	}
	return checkPayloadInput(os.Stdin)
}

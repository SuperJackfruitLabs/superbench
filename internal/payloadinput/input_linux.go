//go:build linux

package payloadinput

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
)

// Seals is the exact seal set: SEAL, SHRINK, GROW and WRITE.
const Seals = 1 | 2 | 4 | 8
const addSeals = 1033
const getSeals = 1034

// Open turns already-bounded opaque bytes into anonymous immutable storage. No
// filesystem path, pipe writer, goroutine or daemon FD survives exec. Empty
// input returns nil: the caller keeps its existing EOF behavior. Like
// close_range, the syscall is supported only on the shipped Linux arches.
func Open(data []byte) (*os.File, error) {
	if len(data) > finiteio.MaxBytes {
		return nil, ErrInput
	}
	if len(data) == 0 {
		return nil, nil
	}
	var number uintptr
	switch runtime.GOARCH {
	case "amd64":
		number = 319
	case "arm64":
		number = 279
	default:
		return nil, ErrInput
	}
	name, err := syscall.BytePtrFromString("superbench-payload-input")
	if err != nil {
		return nil, errors.Join(ErrInput, err)
	}
	fd, _, errno := syscall.Syscall(number, uintptr(unsafe.Pointer(name)), 3, 0) // CLOEXEC | ALLOW_SEALING
	runtime.KeepAlive(name)
	if errno != 0 {
		return nil, errors.Join(ErrInput, errno)
	}
	writable := os.NewFile(fd, "finite payload input")
	defer writable.Close()
	if err := writable.Chmod(0600); err != nil {
		return nil, errors.Join(ErrInput, err)
	}
	n, err := writable.Write(data)
	if err != nil || n != len(data) {
		return nil, errors.Join(ErrInput, err)
	}
	if _, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, addSeals, Seals); errno != 0 {
		return nil, errors.Join(ErrInput, errno)
	}
	// Open this owned descriptor read-only, never a caller-selected pathname.
	readonly, err := os.Open("/proc/self/fd/" + strconv.Itoa(int(fd)))
	if err != nil {
		return nil, errors.Join(ErrInput, err)
	}
	before, a := writable.Stat()
	after, b := readonly.Stat()
	if a != nil || b != nil || !os.SameFile(before, after) || after.Size() != int64(len(data)) {
		readonly.Close()
		return nil, errors.Join(ErrInput, a, b)
	}
	if err := Check(readonly); err != nil {
		readonly.Close()
		return nil, err
	}
	return readonly, nil
}

// Check requires a nonempty bounded regular read-only descriptor carrying
// exactly Seals, positioned at offset zero (never partially consumed).
func Check(f *os.File) error {
	if f == nil {
		return ErrInput
	}
	s, err := f.Stat()
	if err != nil || !s.Mode().IsRegular() || s.Size() <= 0 || s.Size() > finiteio.MaxBytes {
		return errors.Join(ErrInput, err)
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETFL, 0)
	if errno != 0 || flags&syscall.O_ACCMODE != syscall.O_RDONLY {
		return errors.Join(ErrInput, errno)
	}
	seals, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), getSeals, 0)
	if errno != 0 || seals != Seals {
		return errors.Join(ErrInput, errno)
	}
	offset, err := f.Seek(0, 1)
	if err != nil || offset != 0 {
		return errors.Join(ErrInput, err)
	}
	return nil
}

// Bind is called only after all privileged authority/stdio checks and the
// credential drop. The replacement has no capability beyond reading the exact
// sealed task bytes; the bound stdin is rechecked as the same open file.
func Bind(f *os.File) error {
	if err := Check(f); err != nil {
		return err
	}
	// dup3 exists on every Linux architecture; dup2 does not on arm64.
	if err := syscall.Dup3(int(f.Fd()), 0, 0); err != nil {
		return errors.Join(ErrInput, err)
	}
	a, err := f.Stat()
	b, other := os.Stdin.Stat()
	if err != nil || other != nil || !os.SameFile(a, b) {
		return errors.Join(ErrInput, err, other)
	}
	return Check(os.Stdin)
}

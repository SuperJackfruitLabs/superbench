//go:build linux

package registeredinspection

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

var ErrFrame = errors.New("invalid managed payload request frame")

const frameTimeout = 30 * time.Second

// The result is exactly one bounded length-prefixed canonical Layout. A reader
// that disappears or does not drain cannot keep this privileged process alive.
func WriteLayout(output *os.File, raw []byte, timeout time.Duration) (err error) {
	return writeResult(output, raw, MaxLayout, timeout)
}

func WritePreparation(output *os.File, raw []byte, timeout time.Duration) error {
	return writeResult(output, raw, MaxPreparation, timeout)
}

func writeResult(output *os.File, raw []byte, limit int, timeout time.Duration) (err error) {
	if len(raw) == 0 || len(raw) > limit || timeout <= 0 || timeout > frameTimeout {
		return ErrFrame
	}
	fd := output.Fd()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
	if errno != 0 {
		return errors.Join(ErrFrame, errno)
	}
	if _, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFL, flags|syscall.O_NONBLOCK); errno != 0 {
		return errors.Join(ErrFrame, errno)
	}
	defer func() {
		_, _, e := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFL, flags)
		if e != 0 {
			err = errors.Join(ErrFrame, err, e)
		}
	}()
	duplicate, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_DUPFD_CLOEXEC, 3)
	if errno != 0 {
		return errors.Join(ErrFrame, errno)
	}
	writer := os.NewFile(duplicate, "inspection result")
	defer writer.Close()
	if err = writer.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return errors.Join(ErrFrame, err)
	}
	framed := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(framed, uint32(len(raw)))
	copy(framed[4:], raw)
	n, err := writer.Write(framed)
	if err != nil || n != len(framed) {
		return errors.Join(ErrFrame, err, io.ErrShortWrite)
	}
	return nil
}

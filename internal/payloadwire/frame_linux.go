//go:build linux

package payloadwire

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

var ErrFrame = errors.New("invalid managed payload request frame")

const FrameTimeout = 30 * time.Second

// The helper consumes exactly a four-byte length and that many JSON bytes.
// There is no buffered readahead into Git's subsequent stdin. A payload request
// must then reach EOF; public payload RUN has no stdin stream. Git may retain
// subsequent stdin bytes, which its daemon caller must bound and cancel.
func ReadFrame(input *os.File, timeout time.Duration) (request Request, err error) {
	inspection, request, err := ReadInvocation(input, timeout)
	if inspection != nil {
		return Request{}, ErrFrame
	}
	return request, err
}

// readInvocation accepts either the unchanged execution frame or the distinct
// scope-free inspection frame. No unrecognized variant falls back to execution.
func ReadInvocation(input *os.File, timeout time.Duration) (inspection *InspectionRequest, request Request, err error) {
	if timeout <= 0 || timeout > FrameTimeout {
		return nil, Request{}, ErrFrame
	}
	fd := input.Fd()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
	if errno != 0 {
		return nil, Request{}, errors.Join(ErrFrame, errno)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFL, flags|syscall.O_NONBLOCK); errno != 0 {
		return nil, Request{}, errors.Join(ErrFrame, errno)
	}
	defer func() {
		_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFL, flags)
		if errno != 0 {
			inspection = nil
			request = Request{}
			err = errors.Join(ErrFrame, err, errno)
		}
	}()
	duplicate, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_DUPFD_CLOEXEC, 3)
	if errno != 0 {
		return nil, Request{}, errors.Join(ErrFrame, errno)
	}
	reader := os.NewFile(duplicate, "payload request frame")
	defer reader.Close()
	if err := reader.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, Request{}, errors.Join(ErrFrame, err)
	}
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, Request{}, errors.Join(ErrFrame, err)
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > MaxRequest {
		return nil, Request{}, ErrFrame
	}
	raw := make([]byte, int(length))
	if _, err := io.ReadFull(reader, raw); err != nil {
		return nil, Request{}, errors.Join(ErrFrame, err)
	}
	if candidate, parseErr := ParseInspectionRequest(raw); parseErr == nil {
		inspection = &candidate
	} else {
		request, err = ParseRequest(raw)
		if err != nil {
			return nil, Request{}, errors.Join(ErrFrame, err)
		}
	}
	if inspection != nil || request.Kind == "payload" {
		var extra [1]byte
		n, err := reader.Read(extra[:])
		if n != 0 || err != io.EOF {
			return nil, Request{}, errors.Join(ErrFrame, err)
		}
	}
	return inspection, request, nil
}

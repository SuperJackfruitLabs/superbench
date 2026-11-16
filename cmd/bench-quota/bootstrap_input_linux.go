//go:build linux

package main

import (
	"errors"
	"os"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
)

// Bounded canonical JSON followed by EOF. A partial or withheld input cannot
// retain the privileged helper indefinitely. No detached reader goroutine lives
// beyond a timeout; only the helper's own stdin description becomes nonblocking.
func readBootstrapSpec(input *os.File) ([]byte, error) {
	return readBootstrapSpecUntil(input, time.Now().Add(10*time.Second))
}

func readBootstrapSpecUntil(input *os.File, deadline time.Time) ([]byte, error) {
	return readBootstrapInputUntil(input, deadline, payloadbootstrap.MaxSpec)
}

func readBootstrapInputUntil(input *os.File, deadline time.Time, limit int) ([]byte, error) {
	fd := int(input.Fd())
	if fd < 0 || fd >= 1024 {
		return nil, errBootstrapCommand
	}
	info, err := input.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeNamedPipe == 0 {
		return nil, errBootstrapCommand
	}
	if info.Mode().IsRegular() && info.Size() > int64(limit) {
		return nil, errBootstrapCommand
	}
	if err = syscall.SetNonblock(fd, true); err != nil {
		return nil, err
	}
	raw := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		if !time.Now().Before(deadline) {
			return nil, errBootstrapCommand
		}
		n, e := syscall.Read(fd, buf)
		if n > 0 {
			if len(raw)+n > limit {
				return nil, errBootstrapCommand
			}
			raw = append(raw, buf[:n]...)
		}
		if e == nil && n == 0 {
			return raw, nil
		}
		if e == nil || errors.Is(e, syscall.EINTR) {
			continue
		}
		if !errors.Is(e, syscall.EAGAIN) && !errors.Is(e, syscall.EWOULDBLOCK) {
			return nil, e
		}
		var set syscall.FdSet
		set.Bits[fd/64] |= 1 << uint(fd%64)
		wait := syscall.NsecToTimeval(min(time.Until(deadline), 100*time.Millisecond).Nanoseconds())
		if _, e = syscall.Select(fd+1, &set, nil, nil, &wait); e != nil && !errors.Is(e, syscall.EINTR) {
			return nil, e
		}
	}
}

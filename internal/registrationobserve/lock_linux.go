//go:build linux

package registrationobserve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
)

// heldState never creates or adopts a missing lock/database. Quiescence pins
// physical state ancestry and its change history; the independent descriptors
// bind the actual existing host lock and SQL file throughout the observation.
type heldState struct {
	root    string
	quiet   *gitcontrol.Quiescence
	files   []*os.File
	initial []os.FileInfo
}

func holdState(ctx context.Context, root string) (_ *heldState, err error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrObservation
	}
	quiet, err := gitcontrol.Quiescent(root)
	if err != nil {
		return nil, errors.Join(ErrObservation, err)
	}
	h := &heldState{root: root, quiet: quiet}
	defer func() {
		if err != nil {
			h.close()
		}
	}()
	for _, name := range []string{"lock", "bench.db"} {
		f, info, e := openStateFile(root, name)
		if e != nil {
			return nil, errors.Join(ErrObservation, e)
		}
		h.files = append(h.files, f)
		h.initial = append(h.initial, info)
	}
	for {
		err = syscall.Flock(int(h.files[0].Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return nil, errors.Join(ErrObservation, err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errors.Join(ErrObservation, ctx.Err())
		case <-timer.C:
		}
	}
	if err = h.recheck(); err != nil {
		return nil, err
	}
	return h, nil
}

func openStateFile(root, name string) (*os.File, os.FileInfo, error) {
	// Quiescent pins all physical ancestors before and after this named open;
	// O_NOFOLLOW independently refuses a substituted final lock/SQL symlink.
	fd, err := syscall.Open(filepath.Join(root, name), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), "held registration state")
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode() != 0600 || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) || st.Gid != uint32(os.Getegid()) {
		f.Close()
		return nil, nil, ErrObservation
	}
	return f, info, nil
}

func (h *heldState) recheck() error {
	if h == nil || len(h.files) != 2 || h.quiet == nil {
		return ErrObservation
	}
	if err := h.quiet.Recheck(); err != nil {
		return errors.Join(ErrObservation, err)
	}
	for i, name := range []string{"lock", "bench.db"} {
		f, named, err := openStateFile(h.root, name)
		if err != nil {
			return errors.Join(ErrObservation, err)
		}
		f.Close()
		now, err := h.files[i].Stat()
		if err != nil || !os.SameFile(h.initial[i], now) || !os.SameFile(h.initial[i], named) || now.Mode() != h.initial[i].Mode() {
			return ErrObservation
		}
	}
	return h.quiet.Recheck()
}

func (h *heldState) close() {
	if h == nil {
		return
	}
	// Closing the original descriptor releases exactly this shared lock.
	for _, f := range h.files {
		_ = f.Close()
	}
	h.files = nil
	if h.quiet != nil {
		_ = h.quiet.Close()
		h.quiet = nil
	}
}

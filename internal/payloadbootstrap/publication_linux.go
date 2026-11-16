//go:build linux

package payloadbootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
)

const directoryFlags = syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC

// Walk each ancestor without following links. All subsequent creation and
// ownership changes use these descriptors, including when a name is replaced.
func publicationStateRoot(p payloadexec.Policy) (_ *os.File, err error) {
	if !filepath.IsAbs(p.StateRoot) || filepath.Clean(p.StateRoot) != p.StateRoot {
		return nil, ErrBootstrap
	}
	current, err := os.OpenFile("/", directoryFlags, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			current.Close()
		}
	}()
	for _, component := range strings.Split(strings.TrimPrefix(p.StateRoot, "/"), "/") {
		if component == "" {
			return nil, ErrBootstrap
		}
		fd, e := syscall.Openat(int(current.Fd()), component, directoryFlags, 0)
		if e != nil {
			return nil, e
		}
		current.Close()
		current = os.NewFile(uintptr(fd), component)
		info, e := current.Stat()
		if e != nil {
			return nil, e
		}
		st := info.Sys().(*syscall.Stat_t)
		if st.Uid != 0 && st.Uid != p.DaemonUID || info.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return nil, ErrBootstrap
		}
	}
	info, err := current.Stat()
	if err != nil {
		return nil, err
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Uid != p.DaemonUID || st.Gid != p.DaemonGID || st.Mode&07777 != 0700 {
		return nil, ErrBootstrap
	}
	return current, nil
}

func publicationDirectory(state *os.File, p payloadexec.Policy) (_ *os.File, err error) {
	created := true
	if err = syscall.Mkdirat(int(state.Fd()), "payload-layouts", 0700); errors.Is(err, syscall.EEXIST) {
		created = false
	} else if err != nil {
		return nil, err
	}
	fd, err := syscall.Openat(int(state.Fd()), "payload-layouts", directoryFlags, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), "payload-layouts")
	defer func() {
		if err != nil {
			dir.Close()
		}
	}()
	info, err := dir.Stat()
	if err != nil {
		return nil, err
	}
	st := info.Sys().(*syscall.Stat_t)
	uid, gid := p.DaemonUID, p.DaemonGID
	if created {
		uid, gid = uint32(os.Geteuid()), uint32(os.Getegid())
	}
	if st.Uid != uid || st.Gid != gid || st.Mode&07777 != 0700 {
		return nil, ErrBootstrap
	}
	if err = sameDirectoryAt(state, dir); err != nil {
		return nil, err
	}
	if created {
		// Never chown by path, and never repair an existing directory.
		if err = dir.Chown(int(p.DaemonUID), int(p.DaemonGID)); err != nil {
			return nil, err
		}
		if err = dir.Sync(); err != nil {
			return nil, err
		}
		if err = state.Sync(); err != nil {
			return nil, err
		}
	}
	if err = checkPublicationDirectory(state, dir, p); err != nil {
		return nil, err
	}
	return dir, nil
}

func sameDirectoryAt(state, dir *os.File) error {
	fd, err := syscall.Openat(int(state.Fd()), "payload-layouts", directoryFlags, 0)
	if err != nil {
		return err
	}
	named := os.NewFile(uintptr(fd), "payload-layouts")
	defer named.Close()
	a, err := named.Stat()
	if err != nil {
		return err
	}
	b, err := dir.Stat()
	if err != nil || !os.SameFile(a, b) {
		return ErrBootstrap
	}
	return nil
}

func checkPublicationDirectory(state, dir *os.File, p payloadexec.Policy) error {
	current, err := publicationStateRoot(p)
	if err != nil {
		return err
	}
	defer current.Close()
	a, err := current.Stat()
	if err != nil {
		return err
	}
	b, err := state.Stat()
	if err != nil || !os.SameFile(a, b) {
		return ErrBootstrap
	}
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Uid != p.DaemonUID || st.Gid != p.DaemonGID || st.Mode&07777 != 0700 {
		return ErrBootstrap
	}
	return sameDirectoryAt(state, dir)
}

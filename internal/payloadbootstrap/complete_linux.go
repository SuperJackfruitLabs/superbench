//go:build linux

package payloadbootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
)

// Complete is a one-way fresh-tree transfer, not a legacy conversion/recovery
// API. No ownership effect occurs until the full pinned Git vector, private tree
// and completed layout validate. Any interrupted transfer is permanently parked.
func (m *Manager) Complete(ctx context.Context, s Spec) (payloadexec.Layout, error) {
	var result payloadexec.Layout
	if s.Validate() != nil || m == nil || m.store == nil {
		return result, ErrBootstrap
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	err := m.store.Apply(ctx, func() error {
		i, err := m.read(s)
		if err != nil {
			return err
		}
		if err = m.closed(i); err != nil {
			return ErrBootstrap
		}
		t, err := m.capture(i)
		if err != nil {
			return err
		}
		defer t.close()
		if err = t.topology(s); err != nil {
			return err
		}
		if err = m.verifyGit(ctx, s); err != nil {
			return err
		}
		if err = t.check(); err != nil {
			return err
		}
		l, err := t.layout(m, s)
		if err != nil {
			return err
		}
		if err = m.check(); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		i, err = m.read(s)
		if err != nil {
			return err
		}
		if err = m.closed(i); err != nil {
			return err
		}
		i.Phase = "transferring"
		if err = m.write(i, false); err != nil {
			return err
		}
		if err = t.transfer(ctx, m); err != nil {
			return err
		}
		if err = t.check(); err != nil {
			return err
		}
		if err = m.checkRoot(i, m.policy.PayloadUID); err != nil {
			return err
		}
		if err = m.publish(l); err != nil {
			return err
		}
		i.Phase = "completed"
		if err = m.write(i, false); err != nil {
			return err
		}
		result = l
		return nil
	})
	if err != nil {
		return payloadexec.Layout{}, err
	}
	return result, nil
}
func (m *Manager) publish(l payloadexec.Layout) error {
	if err := m.check(); err != nil {
		return err
	}
	// StateRoot is an already protected installed daemon-owned state root. This
	// code creates one fixed metadata child, never a caller-selected ancestry.
	if err := m.checkStateAncestors(); err != nil {
		return err
	}
	info, err := os.Lstat(m.policy.StateRoot)
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	actual, err := filepath.EvalSymlinks(m.policy.StateRoot)
	if err != nil || actual != m.policy.StateRoot || !info.IsDir() || st.Uid != m.policy.DaemonUID || info.Mode().Perm()&0077 != 0 {
		return ErrBootstrap
	}
	dir := filepath.Join(m.policy.StateRoot, "payload-layouts")
	if err = os.Mkdir(dir, 0700); err == nil {
		if err = os.Chown(dir, int(m.policy.DaemonUID), int(m.policy.DaemonGID)); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	parent, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer parent.Close()
	d, err := parent.Stat()
	if err != nil {
		return err
	}
	ds := d.Sys().(*syscall.Stat_t)
	if ds.Uid != m.policy.DaemonUID || ds.Gid != m.policy.DaemonGID || ds.Mode&07777 != 0700 {
		return ErrBootstrap
	}
	raw, err := l.Bytes(m.policy)
	if err != nil {
		return err
	}
	fd, err := syscall.Openat(int(parent.Fd()), l.Key+".json", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), l.Key+".json")
	defer file.Close()
	if err = file.Chmod(0600); err != nil {
		return err
	}
	if _, err = file.Write(raw); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Chown(int(m.policy.DaemonUID), int(m.policy.DaemonGID)); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = parent.Sync(); err != nil {
		return err
	}
	named, err := os.Lstat(dir)
	if err != nil || !os.SameFile(d, named) {
		return ErrBootstrap
	}
	if err = m.checkStateAncestors(); err != nil {
		return err
	}
	namedFile, err := os.Lstat(filepath.Join(dir, l.Key+".json"))
	if err != nil {
		return err
	}
	saved, err := file.Stat()
	if err != nil || !os.SameFile(saved, namedFile) || saved.Size() != int64(len(raw)) {
		return ErrBootstrap
	}
	return m.check()
}

func (m *Manager) checkStateAncestors() error {
	for dir := m.policy.StateRoot; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || st.Uid != 0 && st.Uid != m.policy.DaemonUID || info.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return ErrBootstrap
		}
		if dir == "/" {
			return nil
		}
	}
}

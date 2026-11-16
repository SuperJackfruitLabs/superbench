//go:build linux

package payloadbootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

// Begin permanently binds a fresh quota allocation to this original operation,
// BID, policy and complete source vector. Existing allocations never convert.
// Its initial tree is daemon-private and has never been exposed to payload UID.
func (m *Manager) Begin(ctx context.Context, s Spec) error {
	if s.Validate() != nil || m == nil || m.store == nil {
		return ErrBootstrap
	}
	return m.store.Apply(ctx, func() error {
		// Keep the exact operation and source vector durable before the quota
		// reservation can survive a crash without a bootstrap intent. This is
		// evidence for explicit recovery, never permission to repeat Begin.
		reserved := intent{Spec: s, Operation: m.operation, PolicyDigest: m.digest, Phase: "reserved"}

		_, err := m.storage.ApplyFreshRequested(s.Key, s.QuotaBytes, func() error {
			return m.write(reserved, true)
		}, func(a hoststorage.Assignment) error {
			if err := m.check(); err != nil {
				return err
			}
			info, err := os.Lstat(a.Path)
			if err != nil {
				return err
			}
			i := reserved
			i.Allocation, i.Phase, i.Root = a, "allocating", node(info)
			if err = m.checkRoot(i, 0); err != nil {
				return err
			}
			parent := filepath.Dir(m.intentPath(s))
			if err = os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			if err = storagecustody.RootOwnedDirectory(parent, true); err != nil {
				return err
			}
			if err = os.Mkdir(m.controlState(s), 0700); err != nil {
				return err
			}
			controls, err := os.Lstat(m.controlState(s))
			if err != nil {
				return err
			}
			i.Controls = node(controls)
			if err = m.write(i, false); err != nil {
				return err
			}
			// Only fixed new directories, created exclusively under the just-reserved
			// inode, are transferred. There is no recursive chown or existing-tree walk.
			dirs := []string{".superbench-git", ".superbench-runtime", ".superbench-runtime/.root", ".superbench-runtime/.root/home", ".superbench-runtime/.root/tmp"}
			for _, member := range s.Members {
				base := ".superbench-runtime/" + member.Name
				dirs = append(dirs, base, base+"/home", base+"/tmp")
			}
			for _, relative := range dirs {
				path := filepath.Join(a.Path, relative)
				if err = os.Mkdir(path, 0700); err != nil {
					return err
				}
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				err = f.Chown(int(m.policy.DaemonUID), int(m.policy.DaemonGID))
				if err == nil {
					err = f.Sync()
				}
				_ = f.Close()
				if err != nil {
					return err
				}
			}
			f, err := os.Open(a.Path)
			if err != nil {
				return err
			}
			defer f.Close()
			if nodeMust, err := f.Stat(); err != nil || node(nodeMust) != i.Root {
				return ErrBootstrap
			}
			if err = f.Chown(int(m.policy.DaemonUID), int(m.policy.DaemonGID)); err != nil {
				return err
			}
			if err = f.Sync(); err != nil {
				return err
			}
			if err = m.checkRoot(i, m.policy.DaemonUID); err != nil {
				return err
			}
			i.Phase = "begun"
			return m.write(i, false)
		})
		return err
	})
}

//go:build linux

package payloadexec

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// LoadLayout reads an existing completed registration. It neither creates a
// registration nor converts any existing checkout. The request names only a
// logical key; the policy fixes the daemon-private authority directory.
func LoadLayout(p Policy, r Request) (Layout, error) {
	if err := p.Validate(); err != nil {
		return Layout{}, err
	}
	if err := r.Validate(); err != nil {
		return Layout{}, err
	}
	digest, err := policyDigest(p)
	if err != nil || r.PolicyDigest != digest {
		return Layout{}, ErrPolicy
	}
	directory := filepath.Join(p.StateRoot, "payload-layouts")
	parent, err := openPhysicalDirectory(directory, p.DaemonUID)
	if err != nil {
		return Layout{}, errors.Join(ErrPolicy, err)
	}
	defer parent.Close()
	name := r.Key + ".json"
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return Layout{}, errors.Join(ErrPolicy, err)
	}
	opened := os.NewFile(uintptr(fd), name)
	defer opened.Close()
	before, err := opened.Stat()
	if err != nil {
		return Layout{}, errors.Join(ErrPolicy, err)
	}
	stat := before.Sys().(*syscall.Stat_t)
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || stat.Uid != p.DaemonUID || stat.Nlink != 1 || before.Size() > MaxLayout {
		return Layout{}, ErrPolicy
	}
	raw, err := io.ReadAll(io.LimitReader(opened, MaxLayout+1))
	if err != nil {
		return Layout{}, errors.Join(ErrPolicy, err)
	}
	after, err := opened.Stat()
	if err != nil {
		return Layout{}, errors.Join(ErrPolicy, err)
	}
	named, err := os.Lstat(filepath.Join(directory, name))
	if err != nil || !samePolicyFile(before, after) || !samePolicyFile(before, named) || len(raw) != int(before.Size()) {
		return Layout{}, errors.Join(ErrPolicy, err)
	}
	// Reopen the complete protected ancestry: an old parent descriptor cannot
	// turn a registration in a renamed state directory into current authority.
	current, err := openPhysicalDirectory(directory, p.DaemonUID)
	if err != nil {
		return Layout{}, errors.Join(ErrPolicy, err)
	}
	defer current.Close()
	oldParent, err := parent.Stat()
	if err != nil {
		return Layout{}, errors.Join(ErrPolicy, err)
	}
	newParent, err := current.Stat()
	if err != nil || !os.SameFile(oldParent, newParent) {
		return Layout{}, errors.Join(ErrPolicy, err)
	}
	layout, err := ParseLayout(p, raw)
	if err != nil || layout.Key != r.Key || layout.ID != r.LayoutID || layout.Scalar && r.Root || !layout.Scalar && r.Member == "" && !r.Root {
		return Layout{}, errors.Join(ErrPolicy, err)
	}
	if _, _, _, _, err := layout.paths(p, r.Member); err != nil {
		return Layout{}, err
	}
	return layout, nil
}

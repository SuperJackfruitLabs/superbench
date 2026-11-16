//go:build linux

package payloadexec

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// LoadFixedPolicy never accepts a request-supplied path or an environment
// override. Only protected, root-owned physical ancestors and a bounded regular
// policy file are accepted; reading this policy grants no execution authority.
func LoadFixedPolicy() (Policy, error) { return loadPolicy(FixedPolicy) }

func loadPolicy(path string) (Policy, error) {
	if !directory(path) {
		return Policy{}, ErrPolicy
	}
	parent, err := openPhysicalDirectory(filepath.Dir(path), 0)
	if err != nil {
		return Policy{}, errors.Join(ErrPolicy, err)
	}
	defer parent.Close()
	opened, info, err := openPolicyFile(parent, filepath.Base(path))
	if err != nil {
		return Policy{}, err
	}
	defer opened.Close()
	raw, err := io.ReadAll(io.LimitReader(opened, MaxPolicy+1))
	if err != nil {
		return Policy{}, errors.Join(ErrPolicy, err)
	}
	after, err := opened.Stat()
	if err != nil {
		return Policy{}, errors.Join(ErrPolicy, err)
	}
	named, err := os.Lstat(path)
	if err != nil {
		return Policy{}, errors.Join(ErrPolicy, err)
	}
	if !samePolicyFile(info, after) || !samePolicyFile(info, named) || len(raw) != int(info.Size()) {
		return Policy{}, ErrPolicy
	}
	return ParsePolicy(raw)
}

// O_NONBLOCK precedes fstat: a misconfigured FIFO must refuse instead of
// blocking the privileged helper before the regular-file check can run.
func openPolicyFile(parent *os.File, name string) (*os.File, os.FileInfo, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, errors.Join(ErrPolicy, err)
	}
	opened := os.NewFile(uintptr(fd), name)
	info, err := opened.Stat()
	if err != nil {
		_ = opened.Close()
		return nil, nil, errors.Join(ErrPolicy, err)
	}
	if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Mode&07777 != 0600 || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Sys().(*syscall.Stat_t).Nlink != 1 || info.Size() > MaxPolicy {
		_ = opened.Close()
		return nil, nil, ErrPolicy
	}
	return opened, info, nil
}

func samePolicyFile(a, b os.FileInfo) bool { return registeredinspection.SamePolicyFile(a, b) }

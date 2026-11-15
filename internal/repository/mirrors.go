// Package repository manages the shared bare mirror without changing task worktrees.
package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Mirrors struct {
	State, Data string
	Timeout     time.Duration
}

const DefaultTimeout = 2 * time.Minute

func Path(data, repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(data, "mirrors", hex.EncodeToString(sum[:])+".git")
}
func (m Mirrors) deadline(ctx context.Context) (context.Context, context.CancelFunc, error) {
	timeout := m.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 || timeout > time.Hour {
		return nil, nil, errors.New("invalid mirror timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	return ctx, cancel, nil
}

// Prepare publishes only a complete bare clone. Interrupted staging directories
// stay unreferenced and quota-accounted. The caller serializes mirror mutations.
func (m Mirrors) Prepare(ctx context.Context, repo string) (string, error) {
	ctx, cancel, e := m.deadline(ctx)
	if e != nil {
		return "", e
	}
	defer cancel()

	parent := filepath.Join(m.Data, "mirrors")
	if e := os.MkdirAll(parent, 0700); e != nil {
		return "", e
	}
	info, e := os.Lstat(parent)
	if e != nil {
		return "", e
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("mirror parent must be a physical directory")
	}
	mirror := Path(m.Data, repo)
	if _, e := os.Lstat(mirror); os.IsNotExist(e) {
		if e := m.clone(ctx, repo, mirror); e != nil {
			return "", e
		}
	} else if e != nil {
		return "", e
	}
	if e := m.refresh(ctx, repo, mirror); e != nil {
		return "", e
	}
	return mirror, nil
}
func (m Mirrors) Refresh(ctx context.Context, repo, mirror string) error {
	ctx, cancel, e := m.deadline(ctx)
	if e != nil {
		return e
	}
	defer cancel()
	return m.refresh(ctx, repo, mirror)
}
func (m Mirrors) location(repo, mirror string) error {
	if mirror != Path(m.Data, repo) {
		return errors.New("mirror path differs from registered repository")
	}
	parent, e := os.Lstat(filepath.Dir(mirror))
	if e != nil {
		return e
	}
	if !parent.IsDir() {
		return errors.New("mirror parent must be a physical directory")
	}
	stat, e := os.Lstat(mirror)
	if e != nil {
		return e
	}
	if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		return errors.New("mirror path is not a directory")
	}
	return nil
}
func (m Mirrors) refresh(ctx context.Context, repo, mirror string) error {
	if e := m.location(repo, mirror); e != nil {
		return e
	}
	bare, e := git(ctx, mirror, "rev-parse", "--is-bare-repository")
	if e != nil {
		return e
	}
	if strings.TrimSpace(bare) != "true" {
		return errors.New("mirror is not a bare repository")
	}
	origin, e := git(ctx, mirror, "config", "--null", "--get-all", "remote.origin.url")
	if e != nil {
		return e
	}
	if origin != repo+"\x00" {
		return errors.New("mirror origin differs from requested repository")
	}
	// Remote-tracking refs never overwrite a branch checked out in another bench.
	if _, e = git(ctx, mirror, "fetch", "--atomic", "--prune", "--no-prune-tags", "--no-tags", "--no-auto-maintenance", "--recurse-submodules=no", "--refmap=", "origin", "+refs/heads/*:refs/remotes/origin/*"); e != nil {
		return e
	}
	return nil
}

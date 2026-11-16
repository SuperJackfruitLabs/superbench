//go:build linux

package payloadbootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

type boundedOutput struct {
	mu       sync.Mutex
	text     strings.Builder
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := (1 << 20) - b.text.Len()
	if len(p) > remaining {
		b.overflow = true
		b.text.Write(p[:remaining])
	} else {
		b.text.Write(p)
	}
	return len(p), nil
}

// gitCommand creates only a hardened ordinary-daemon command; it never starts it.
// All execution must pass runScoped's actual durable cgroup/fence runner.
func (m *Manager) gitCommand(ctx context.Context, dir string, args ...string) (*exec.Cmd, *boundedOutput, error) {
	if err := m.check(); err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	fixed := []string{"--no-optional-locks", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "protocol.ext.allow=never", "-c", "protocol.file.allow=always"}
	cmd := exec.Command("/usr/bin/git", append(fixed, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_NOSYSTEM=1", "GIT_TEMPLATE_DIR=/dev/null"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: m.policy.DaemonUID, Gid: m.policy.DaemonGID, Groups: []uint32{}}}
	output := &boundedOutput{}
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd, output, nil
}

// Materialize uses independent Git admins and object databases. A daemon mirror
// is a source only: it never becomes the payload's writable common Git directory.
func (m *Manager) Materialize(ctx context.Context, s Spec) error {
	if s.Validate() != nil || m == nil || m.store == nil {
		return ErrBootstrap
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return m.store.Apply(ctx, func() error {
		i, err := m.read(s)
		if err != nil {
			return err
		}
		if i.Phase != "begun" {
			return ErrBootstrap
		}
		if err = m.checkRoot(i, m.policy.DaemonUID); err != nil {
			return err
		}
		i.Phase = "staging"
		if err = m.write(i, false); err != nil {
			return err
		}
		for _, member := range s.Members {
			mirror := repository.Path(m.policy.DataRoot, member.Repository)
			for _, check := range []struct {
				args []string
				want string
			}{
				{[]string{"rev-parse", "--is-bare-repository"}, "true\n"},
				{[]string{"config", "--null", "--get-all", "remote.origin.url"}, member.Repository + "\x00"},
				{[]string{"rev-parse", "--show-object-format=storage"}, member.ObjectFormat + "\n"},
				{[]string{"rev-parse", "--verify", member.Source + "^{commit}"}, member.Source + "\n"},
			} {
				out, e := m.runScoped(ctx, s, mirror, check.args...)
				if e != nil || out != check.want {
					return ErrBootstrap
				}
			}
			checkout := filepath.Join(m.root(s), member.Name)
			admin := filepath.Join(m.root(s), ".superbench-git", member.Name)
			if _, err = m.runScoped(ctx, s, m.root(s), "clone", "--template=", "--no-local", "--no-hardlinks", "--no-checkout", "--separate-git-dir", admin, "--", mirror, checkout); err != nil {
				return err
			}
			if _, err = m.runScoped(ctx, s, checkout, "config", "--local", "remote.origin.url", member.Repository); err != nil {
				return err
			}
			if _, err = m.runScoped(ctx, s, checkout, "checkout", "--no-guess", "-b", member.Branch, member.Source, "--"); err != nil {
				return err
			}
		}
		if err = m.verifyGit(ctx, s); err != nil {
			return err
		}
		latest, err := m.read(s)
		if err != nil {
			return err
		}
		if err = m.clearControls(latest); err != nil {
			return err
		}
		current, err := execution.CurrentIdentity()
		if err != nil {
			return err
		}
		raw, err := s.Bytes()
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		latest.Materialized = &materialized{hex.EncodeToString(sum[:]), m.digest, current.HostID, current.BootID}
		latest.Phase = "staged"
		return m.write(latest, false)
	})
}
func (m *Manager) verifyGit(ctx context.Context, s Spec) error {
	for _, member := range s.Members {
		checkout := filepath.Join(m.root(s), member.Name)
		admin := filepath.Join(m.root(s), ".superbench-git", member.Name)
		checks := []struct {
			args []string
			want string
		}{
			{[]string{"rev-parse", "--path-format=absolute", "--show-toplevel"}, checkout + "\n"},
			{[]string{"rev-parse", "--path-format=absolute", "--git-common-dir"}, admin + "\n"},
			{[]string{"symbolic-ref", "--quiet", "HEAD"}, "refs/heads/" + member.Branch + "\n"},
			{[]string{"rev-parse", "--verify", "HEAD^{commit}"}, member.Source + "\n"},
			{[]string{"rev-parse", "--show-object-format=storage"}, member.ObjectFormat + "\n"},
			{[]string{"config", "--null", "--get-all", "remote.origin.url"}, member.Repository + "\x00"},
			{[]string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching"}, ""},
		}
		for _, check := range checks {
			out, err := m.runScoped(ctx, s, checkout, check.args...)
			if err != nil || out != check.want {
				return ErrBootstrap
			}
		}
		if _, err := m.runScoped(ctx, s, checkout, "fsck", "--full", "--no-reflogs"); err != nil {
			return err
		}
	}
	return nil
}

//go:build linux

package payloadbootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
)

type materialized struct{ SpecSHA256, PolicyDigest, HostID, BootID string }

func (m *Manager) controlState(s Spec) string {
	return filepath.Join(m.storage.Ledger, "payload-bootstrap", s.Key+".controls")
}
func (m *Manager) clearControls(i intent) error {
	info, err := os.Lstat(m.controlState(i.Spec))
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || st.Uid != 0 || st.Mode&07777 != 0700 || node(info) != i.Controls {
		return ErrBootstrap
	}
	child := filepath.Join(m.controlState(i.Spec), "git-controls")
	info, err = os.Lstat(child)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm() != 0700 {
		return ErrBootstrap
	}
	f, err := os.OpenFile(child, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if len(names) != 0 {
		return ErrBootstrap
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
func (m *Manager) closed(i intent) error {
	if i.Phase != "staged" || i.Materialized == nil || !i.CommandCompleted {
		return ErrBootstrap
	}
	raw, err := i.Spec.Bytes()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	current, err := execution.CurrentIdentity()
	if err != nil {
		return err
	}
	want := materialized{hex.EncodeToString(sum[:]), m.digest, current.HostID, current.BootID}
	if *i.Materialized != want {
		return ErrBootstrap
	}
	return m.clearControls(i)
}
func (m *Manager) runScoped(ctx context.Context, s Spec, dir string, args ...string) (string, error) {
	i, err := m.read(s)
	if err != nil {
		return "", err
	}
	if i.Phase != "staging" && i.Phase != "staged" {
		return "", ErrBootstrap
	}
	if err = m.clearControls(i); err != nil {
		return "", err
	}
	cmd, output, err := m.gitCommand(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	// Exact command intent precedes the Runner's own durable scope and child.
	raw, err := json.Marshal(struct {
		Directory              string
		Arguments, Environment []string
		PolicyDigest           string
	}{dir, cmd.Args, cmd.Env, m.digest})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	i.CommandSHA256 = hex.EncodeToString(sum[:])
	i.CommandCompleted = false
	if err = m.write(i, false); err != nil {
		return "", err
	}
	// Host.Start preserves the explicit daemon Credential while injecting the
	// durable Runner scope. No root-credential Git fallback is permitted.
	runner := gitcontrol.Runner{State: m.controlState(s), Root: m.policy.CgroupRoot, Timeout: 2 * time.Minute}
	if err = runner.Run(ctx, cmd); err != nil {
		return "", err
	}
	if output.overflow {
		return "", ErrBootstrap
	}
	if err = m.clearControls(i); err != nil {
		return "", err
	}
	i.CommandCompleted = true
	if err = m.write(i, false); err != nil {
		return "", err
	}
	return output.text.String(), nil
}

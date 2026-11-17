//go:build linux

package payloadbootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

const maxIntent = 128 << 10

type intent struct {
	Spec                Spec
	Allocation          quotahelper.Assignment
	Operation           quotaoperation.Identity
	PolicyDigest, Phase string
	Root, Controls      payloadexec.Node
	Materialized        *materialized
	CommandSHA256       string
	CommandCompleted    bool
}

func node(info os.FileInfo) payloadexec.Node {
	st := info.Sys().(*syscall.Stat_t)
	return payloadexec.Node{Device: uint64(st.Dev), Inode: st.Ino}
}
func (m *Manager) read(s Spec) (intent, error) {
	var i intent
	if s.Validate() != nil || m.check() != nil {
		return i, ErrBootstrap
	}
	parent := filepath.Dir(m.intentPath(s))
	if err := storagecustody.RootOwnedDirectory(parent, true); err != nil {
		return i, err
	}
	f, err := os.OpenFile(m.intentPath(s), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return i, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return i, err
	}
	st := before.Sys().(*syscall.Stat_t)
	if !before.Mode().IsRegular() || st.Uid != 0 || st.Nlink != 1 || st.Mode&07777 != 0600 || before.Size() > maxIntent {
		return i, ErrBootstrap
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxIntent+1))
	if err != nil {
		return i, err
	}
	if len(raw) > maxIntent || json.Unmarshal(raw, &i) != nil {
		return intent{}, ErrBootstrap
	}
	canonical, err := json.Marshal(i)
	if err != nil || !bytes.Equal(raw, canonical) || m.validateIntent(s, i) != nil {
		return intent{}, ErrBootstrap
	}
	after, err := f.Stat()
	if err != nil {
		return intent{}, err
	}
	named, err := os.Lstat(m.intentPath(s))
	if err != nil || !os.SameFile(before, after) || !os.SameFile(before, named) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return intent{}, ErrBootstrap
	}
	return i, nil
}
func (m *Manager) write(i intent, first bool) error {
	if err := m.check(); err != nil {
		return err
	}
	parent := filepath.Dir(m.intentPath(i.Spec))
	if first {
		if err := os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		ledger, err := os.Open(filepath.Dir(parent))
		if err != nil {
			return err
		}
		err = ledger.Sync()
		closeErr := ledger.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := storagecustody.RootOwnedDirectory(parent, true); err != nil {
		return err
	}
	raw, err := json.Marshal(i)
	if err != nil || len(raw) > maxIntent {
		return ErrBootstrap
	}
	path := m.intentPath(i.Spec)
	target := path
	if !first {
		target = path + ".transition"
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	// On any failure the partial file is retained. It never authorizes cleanup,
	// a second stage, or legacy Apply. Operator repair must inspect it explicitly.
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if !first {
		if err = os.Rename(target, path); err != nil {
			return err
		}
	}
	d, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return err
	}
	return m.check()
}
func (m *Manager) checkRoot(i intent, owner uint32) error {
	info, err := os.Lstat(m.root(i.Spec))
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || st.Uid != owner || st.Mode&07777 != 0700 || node(info) != i.Root {
		return ErrBootstrap
	}
	return nil
}

// A reserved intent carries provenance only. It cannot assert any allocation,
// physical node, Git result or ownership transfer before those effects exist.
func (m *Manager) validateIntent(s Spec, i intent) error {
	if m == nil || !m.operation.Valid() || !sha256Pattern.MatchString(m.digest) || s.Validate() != nil || !reflect.DeepEqual(i.Spec, s) || i.Operation != m.operation && !reclaimable(m.reclaim, i.Operation, m.operation, i.Phase) || i.PolicyDigest != m.digest || i.CommandSHA256 != "" && !sha256Pattern.MatchString(i.CommandSHA256) || i.CommandCompleted && i.CommandSHA256 == "" {
		return ErrBootstrap
	}
	if i.Phase == "reserved" {
		if i.Allocation != (quotahelper.Assignment{}) || i.Root != (payloadexec.Node{}) || i.Controls != (payloadexec.Node{}) || i.Materialized != nil || i.CommandSHA256 != "" || i.CommandCompleted {
			return ErrBootstrap
		}
		return nil
	}
	if i.Root.Inode == 0 || i.Controls.Inode == 0 || !m.validAllocation(s, i.Allocation) {
		return ErrBootstrap
	}
	switch i.Phase {
	case "allocating", "begun", "staging", "materialized_unfenced", "staged", "transferring", "completed", "reclaiming", "reclaimed":
		return nil
	default:
		return ErrBootstrap
	}
}

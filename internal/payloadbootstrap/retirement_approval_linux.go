//go:build linux

package payloadbootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

const MaxRetirement = 128 << 10

// ParseRetirement parses the complete bounded reviewed input before any lock.
// The manager independently validates it against the retained original intent.
func ParseRetirement(raw []byte) (Spec, RetirementPlan, error) {
	var request struct {
		Spec Spec
		Plan RetirementPlan
	}
	if len(raw) > MaxRetirement || json.Unmarshal(raw, &request) != nil {
		return Spec{}, RetirementPlan{}, ErrBootstrap
	}
	canonical, err := json.Marshal(request)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(canonical, compact.Bytes()) || request.Spec.Validate() != nil {
		return Spec{}, RetirementPlan{}, ErrBootstrap
	}
	return request.Spec, request.Plan, nil
}

func (m *Manager) retainRetirement(plan RetirementPlan) error {
	if err := m.check(); err != nil {
		return err
	}
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) > MaxRetirement {
		return ErrBootstrap
	}
	if err := retainRetirementFile(m.storage.Ledger, plan.Bootstrap.Key, raw); err != nil {
		return err
	}
	return m.check()
}

func retainRetirementFile(ledgerPath, key string, raw []byte) error {
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || !name.MatchString(key) || len(raw) == 0 || len(raw) > MaxRetirement {
		return ErrBootstrap
	}
	if err := storagecustody.RootOwnedDirectory(ledgerPath, true); err != nil {
		return err
	}
	parent := filepath.Join(ledgerPath, "payload-retirements")
	if err := os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := storagecustody.RootOwnedDirectory(parent, true); err != nil {
		return err
	}
	ledger, err := os.Open(ledgerPath)
	if err != nil {
		return err
	}
	defer ledger.Close()
	if err = ledger.Sync(); err != nil {
		return err
	}
	path := filepath.Join(parent, key+".json")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	first := err == nil
	if errors.Is(err, os.ErrExist) {
		f, err = os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return err
	}
	st := before.Sys().(*syscall.Stat_t)
	if !before.Mode().IsRegular() || st.Uid != 0 || st.Nlink != 1 || st.Mode&07777 != 0600 {
		return ErrBootstrap
	}
	if first {
		if n, e := f.Write(raw); e != nil {
			return e
		} else if n != len(raw) {
			return io.ErrShortWrite
		}
	} else {
		saved, e := io.ReadAll(io.LimitReader(f, MaxRetirement+1))
		if e != nil {
			return e
		}
		if !bytes.Equal(saved, raw) {
			return ErrBootstrap
		}
	}
	// Re-establish durability even on an exact retry after a lost acknowledgement.
	if err = f.Sync(); err != nil {
		return err
	}
	named, err := os.Lstat(path)
	after, e := f.Stat()
	if err != nil {
		return err
	}
	if e != nil {
		return e
	}
	if !os.SameFile(before, named) || !os.SameFile(before, after) || after.Size() != int64(len(raw)) || after.Mode() != before.Mode() {
		return ErrBootstrap
	}
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return err
	}
	if err = storagecustody.RootOwnedDirectory(parent, true); err != nil {
		return err
	}
	return nil
}

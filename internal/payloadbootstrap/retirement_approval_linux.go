//go:build linux

package payloadbootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retainedfile"
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

// retainRetirementFile publishes the reviewed plan once under the ledger. An
// exact retry is accepted; any other existing file refuses.
func retainRetirementFile(ledgerPath, key string, raw []byte) error {
	if retainedfile.DirectRoot() != nil || !name.MatchString(key) || len(raw) == 0 || len(raw) > MaxRetirement {
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
	if err = retainedfile.Publish(parent, key+".json", raw, MaxRetirement); errors.Is(err, retainedfile.ErrRefused) {
		return ErrBootstrap
	}
	return err
}

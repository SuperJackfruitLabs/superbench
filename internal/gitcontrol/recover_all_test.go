package gitcontrol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
)

func TestGlobalRecoveryPreservesAttributedInventoryBeforeAnyEffect(t *testing.T) {
	state := canonicalTemp(t)
	path, first := fixtureIntent(t, state, state)
	other := first
	other.ID = strings.Repeat("d", 32)
	other.OperationID = strings.Repeat("e", 64)
	other.OperationEpoch = strings.Repeat("f", 32)
	if err := saveControl(filepath.Dir(path), other); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	effects := 0
	err = recoverAll(state, func(string) error { effects++; return nil }, func(*execution.Execution) error { effects++; return nil })
	if err == nil || effects != 0 {
		t.Fatal("global recovery crossed operation ownership", effects, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("legacy journal changed before refusal", err)
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(path), other.ID+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalRecoveryPreservesLegacyReconciliation(t *testing.T) {
	state := canonicalTemp(t)
	path, _ := fixtureIntent(t, state, state)
	validations, fences := 0, 0
	if err := recoverAll(state, func(string) error { validations++; return nil }, func(*execution.Execution) error { fences++; return nil }); err != nil {
		t.Fatal(err)
	}
	if validations != 1 || fences != 1 {
		t.Fatal(validations, fences)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("legacy journal not reconciled", err)
	}
}

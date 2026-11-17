package gitcontrol

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestFencedAcceptsOnlyEndedControlsAndChangesNothing(t *testing.T) {
	ok := func(string) error { return nil }
	empty := func(*execution.Execution) error { return nil }
	absent := filepath.Join(canonicalTemp(t), "missing")
	if err := fenced(absent, ok, empty); err != nil {
		t.Fatal("absent state refused", err)
	}
	state := canonicalTemp(t)
	if err := fenced(state, ok, empty); err != nil {
		t.Fatal("absent journal refused", err)
	}
	if _, err := controlDir(state, true); err != nil {
		t.Fatal(err)
	}
	if err := fenced(state, ok, empty); err != nil {
		t.Fatal("empty journal refused", err)
	}
	path, _ := fixtureIntent(t, state, state)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	if err := fenced(state, ok, func(*execution.Execution) error { checked++; return nil }); err != nil || checked != 1 {
		t.Fatal("ended control refused", checked, err)
	}
	populated := errors.New("execution cgroup still populated")
	if err := fenced(state, ok, func(*execution.Execution) error { return populated }); !errors.Is(err, ErrNotFenced) || !errors.Is(err, populated) {
		t.Fatal("populated scope admitted", err)
	}
	invalid := errors.New("invalid root")
	if err := fenced(state, func(string) error { return invalid }, empty); !errors.Is(err, invalid) {
		t.Fatal("invalid root admitted", err)
	}
	other := filepath.Join(filepath.Dir(path), strings.Repeat("d", 32)+".json")
	attributed := `{"Version":"` + controlVersion + `","Root":"` + state + `","ID":"` + strings.Repeat("d", 32) + `","HostID":"` + strings.Repeat("b", 64) + `","BootID":"b","OperationID":"` + strings.Repeat("e", 64) + `","OperationEpoch":"` + strings.Repeat("f", 32) + `"}`
	if err := os.WriteFile(other, []byte(attributed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := fenced(state, ok, empty); !errors.Is(err, operationcontrol.ErrBusy) {
		t.Fatal("operation-attributed control admitted", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("a fenced check changed the journal", err)
	}
}

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

func TestOperationRecoveryPreservesConcurrentOtherOperation(t *testing.T) {
	state := canonicalTemp(t)
	path, first := fixtureIntent(t, state, state)
	first.OperationID = strings.Repeat("a", 64)
	first.OperationEpoch = strings.Repeat("b", 32)
	writeIntent := func(j controlIntent) {
		t.Helper()
		if err := os.Remove(filepath.Join(filepath.Dir(path), j.ID+".json")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := saveControl(filepath.Dir(path), j); err != nil {
			t.Fatal(err)
		}
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{RequestID: "first", Digest: strings.Repeat("c", 64)}, Key: "card", Operation: "create", Epoch: first.OperationEpoch}
	first.OperationID = i.Target.ID()
	writeIntent(first)
	other := first
	other.ID = strings.Repeat("d", 32)
	other.OperationID = strings.Repeat("e", 64)
	other.OperationEpoch = strings.Repeat("f", 32)
	other.Root = filepath.Join(state, "unrelated-unavailable-root")
	writeIntent(other)
	c := operationcontrol.Control{State: state, Root: first.Root, HostID: first.HostID, BootID: first.BootID}
	var fenced []string
	err := recoverOperation(state, c, i, func(root string) error {
		if root != first.Root {
			t.Fatal(root)
		}
		return nil
	}, func(e *execution.Execution) error { fenced = append(fenced, e.ID); return nil })
	if err != nil || len(fenced) != 1 || fenced[0] != first.ID {
		t.Fatal(fenced, err)
	}
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("exact operation journal retained", err)
	}
	if _, err = os.Lstat(filepath.Join(filepath.Dir(path), other.ID+".json")); err != nil {
		t.Fatal("concurrent operation journal changed", err)
	}
}

func TestOperationRecoveryWholeInventoryRefusalPreservesJournals(t *testing.T) {
	for _, mutation := range []string{"legacy", "wrong_epoch", "wrong_root", "wrong_host", "wrong_boot", "malformed"} {
		t.Run(mutation, func(t *testing.T) {
			state := canonicalTemp(t)
			path, j := fixtureIntent(t, state, state)
			i := operationcontrol.Intent{Target: operationcontrol.Target{RequestID: "target", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
			j.OperationID = i.Target.ID()
			j.OperationEpoch = i.Epoch
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := saveControl(filepath.Dir(path), j); err != nil {
				t.Fatal(err)
			}
			other := j
			other.ID = strings.Repeat("c", 32)
			switch mutation {
			case "legacy":
				other.Version = legacyControlVersion
				other.OperationID = ""
				other.OperationEpoch = ""
			case "wrong_epoch":
				other.OperationEpoch = strings.Repeat("d", 32)
			case "wrong_root":
				other.Root = filepath.Join(state, "changed")
			case "wrong_host":
				other.HostID = strings.Repeat("e", 64)
			case "wrong_boot":
				other.BootID = "different"
			}
			if err := saveControl(filepath.Dir(path), other); err != nil {
				t.Fatal(err)
			}
			if mutation == "malformed" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), other.ID+".json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			c := operationcontrol.Control{State: state, Root: j.Root, HostID: j.HostID, BootID: j.BootID}
			fences := 0
			err = recoverOperation(state, c, i, func(string) error { return nil }, func(*execution.Execution) error { fences++; return errors.New("must not fence") })
			after, e := os.ReadFile(path)
			if err == nil || fences != 0 || e != nil || string(before) != string(after) {
				t.Fatal("ambiguous inventory caused effects", fences, err, e)
			}
		})
	}
}

func TestOperationRecoveryFailedFenceRetainsExactIntent(t *testing.T) {
	state := canonicalTemp(t)
	path, j := fixtureIntent(t, state, state)
	i := operationcontrol.Intent{Target: operationcontrol.Target{RequestID: "fence", Digest: strings.Repeat("e", 64)}, Key: "card", Operation: "setup", Epoch: strings.Repeat("f", 32)}
	j.OperationID = i.Target.ID()
	j.OperationEpoch = i.Epoch
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := saveControl(filepath.Dir(path), j); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c := operationcontrol.Control{State: state, Root: j.Root, HostID: j.HostID, BootID: j.BootID}
	failed := errors.New("writer not fenced")
	err = recoverOperation(state, c, i, func(string) error { return nil }, func(*execution.Execution) error { return failed })
	after, e := os.ReadFile(path)
	if !errors.Is(err, failed) || e != nil || string(before) != string(after) {
		t.Fatal("failed fence lost evidence", err, e)
	}
}

package gitcontrol

import (
	"errors"
	"fmt"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// ErrNotFenced reports a Git control whose scope may still hold a process.
var ErrNotFenced = errors.New("Git control is not fenced")

// Fenced is the read-only form of Recover: it proves that no Git control under
// state can still hold a writer, without fencing, killing or removing anything.
// state and its journal may be absent or empty. Otherwise every intent must be
// unattributed, as Recover requires, name a valid cgroup root, and have an empty
// scope: gone, unpopulated, or from an earlier boot. The intents stay as
// evidence. The caller must hold the lock that excludes new controls.
func Fenced(state string) error {
	host := execution.Host{}
	return fenced(state, validateRoot, host.Empty)
}

func fenced(state string, validate func(string) error, empty func(*execution.Execution) error) error {
	_, intents, err := recoveryInventory(state)
	if err != nil {
		return err
	}
	for _, j := range intents {
		if j.OperationID != "" {
			return operationcontrol.ErrBusy
		}
	}
	for _, j := range intents {
		if err = validate(j.Root); err != nil {
			return err
		}
		if err = empty(j.execution()); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrNotFenced, j.ID, err)
		}
	}
	return nil
}

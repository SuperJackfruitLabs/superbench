package gitcontrol

import (
	"errors"
	"fmt"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// RecoverOperation reconciles only the exact admitted operation's Git controls.
// The caller must serialize its store and prevent any further operation effects
// before calling. Unattributed controls cannot prove cancellation completion.
// Configuration alone never certifies a fence: every selected scope is fenced
// using its original durable host/boot identity before its journal is removed.
func RecoverOperation(state string, c operationcontrol.Control, i operationcontrol.Intent) error {
	host := execution.Host{}
	return recoverOperation(state, c, i, validateRoot, host.Fence)
}

func recoverOperation(state string, c operationcontrol.Control, i operationcontrol.Intent, validate func(string) error, fence func(*execution.Execution) error) error {
	if !i.Valid() || !c.Managed() || state != c.State {
		return operationcontrol.ErrAuthority
	}
	if info, err := os.Lstat(state); err != nil || !info.IsDir() {
		return errors.New("operation control state unavailable")
	}
	dir, intents, err := recoveryInventory(state)
	if err != nil {
		return err
	}
	var selected []controlIntent
	for _, j := range intents {
		if j.OperationID == "" {
			return errors.New("unattributed Git control prevents exact operation recovery")
		}
		if j.OperationID != i.Target.ID() {
			continue
		}
		if j.OperationEpoch != i.Epoch || j.Root != c.Root || j.HostID != c.HostID || j.BootID != c.BootID {
			return operationcontrol.ErrAuthority
		}
		selected = append(selected, j)
	}
	// Preflight the entire selected vector before fencing or removing any intent.
	for _, j := range selected {
		if err = validate(j.Root); err != nil {
			return err
		}
	}
	for _, j := range selected {
		if err = fence(j.execution()); err != nil {
			return fmt.Errorf("operation Git control fence failed; intent retained: %w", err)
		}
		if err = removeControl(dir, j.ID); err != nil {
			return err
		}
	}
	return nil
}

package workspacepolicy

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// ValidateSchema25 retains the old materialization lease representation: older
// binaries cannot adopt the new stopped partial-materialization capability.
func ValidateSchema25(b *model.Bench) error {
	if err := Validate(b); err != nil {
		return err
	}
	if b.Workspace.Phase == "materializing" && b.Lease != nil && b.Lease.StopRequested {
		return errors.New("stopped materialization requires current state schema")
	}
	return nil
}

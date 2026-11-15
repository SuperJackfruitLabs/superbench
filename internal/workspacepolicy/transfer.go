package workspacepolicy

import (
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func validateTransfer(b *model.Bench) error {
	w := b.Workspace
	invalid := errors.New("invalid workspace task-ref transfer authority")
	if t := w.Transfer; t != nil {
		if !key.MatchString(t.ID) || !digestPattern.MatchString(t.Digest) || !key.MatchString(t.TargetKey) || t.TargetKey == b.Key || !identity.MatchString(t.TargetID) || t.TargetID == w.ID || !key.MatchString(t.CreationID) || t.Phase != "relinquishing" && t.Phase != "transferred" || w.Phase != "ready" && w.Archive == nil || b.Lease != nil || w.Pending != nil || w.Stage != nil || w.Checkpoint == nil || t.Phase == "relinquishing" && (w.Phase != "ready" || w.Archive != nil) {
			return invalid
		}
	}
	if t := w.TransferredFrom; t != nil {
		if !key.MatchString(t.ID) || !digestPattern.MatchString(t.Digest) || !key.MatchString(t.SourceKey) || t.SourceKey == b.Key || !identity.MatchString(t.SourceID) || t.SourceID == w.ID {
			return invalid
		}
	}
	return nil
}

// ValidateSchema20 rejects transfer authority unknown to prior state versions.
func ValidateSchema20(b *model.Bench) error {
	if err := Validate(b); err != nil {
		return err
	}
	if b.Workspace.Transfer != nil || b.Workspace.TransferredFrom != nil {
		return errors.New("task-ref transfer requires current state schema")
	}
	return nil
}

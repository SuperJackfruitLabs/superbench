package workspacepolicy

import (
	"errors"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func validateTransferRecovery(b *model.Bench) error {
	r := b.Workspace.Transfer.Recovery
	invalid := errors.New("invalid missing-checkout transfer retirement evidence")
	at, err := time.Parse(time.RFC3339Nano, r.RetiredAt)
	_, offset := at.Zone()
	if r.Mode != "missing" || !digestPattern.MatchString(r.SourceRowDigest) || err != nil || at.IsZero() || offset != 0 || len(r.Runs) > 10000 {
		return invalid
	}
	runs := map[string]bool{}
	for i, run := range r.Runs {
		if !identity.MatchString(run.ID) || !digestPattern.MatchString(run.Digest) || i > 0 && r.Runs[i-1].ID >= run.ID {
			return invalid
		}
		runs[run.ID] = true
	}
	if r.Holder != "" && !boundedText(r.Holder, 200) {
		return invalid
	}
	if e := r.Execution; e != nil {
		if r.Holder == "" || !identity.MatchString(e.ID) || !physicalPath(e.Root) || !physicalPath(e.Log) || !boundedText(e.HostID, 128) || !boundedText(e.BootID, 128) || e.Phase != "stopped" || e.PID < 0 || e.LogLimitBytes < 1024 || e.LogLimitBytes > 1<<30 || !runs[e.ID] {
			return invalid
		}
	}
	if stage := r.Stage; stage != nil {
		known := false
		for _, m := range b.Workspace.Members {
			known = known || m.Name == stage.Name
		}
		if r.Execution == nil || !known || !digestPattern.MatchString(stage.Digest) {
			return invalid
		}
	}
	return nil
}

// ValidateSchema21 preserves the historical transfer format without retirement.
func ValidateSchema21(b *model.Bench) error {
	if err := Validate(b); err != nil {
		return err
	}
	if b.Workspace.Transfer != nil && b.Workspace.Transfer.Recovery != nil {
		return errors.New("missing-checkout transfer retirement requires current state schema")
	}
	return nil
}

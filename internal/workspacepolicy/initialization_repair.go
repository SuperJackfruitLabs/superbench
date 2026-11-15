package workspacepolicy

import (
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func validateInitializationRepair(b *model.Bench) error {
	r := b.Workspace.InitializationRepair
	if r == nil {
		return nil
	}
	if !key.MatchString(r.ID) || !digestPattern.MatchString(r.Digest) || r.PreviousID != "" && (!key.MatchString(r.PreviousID) || r.PreviousID == r.ID) || len(r.RetiredRuns) > 10000 || b.Workspace.Phase == "materializing" {
		return errors.New("invalid initialization repair marker")
	}
	held := map[string]bool{}
	members := map[string]bool{}
	for _, m := range b.Workspace.Members {
		members[m.Name] = true
	}
	if len(r.UncertainOnce) > len(members) {
		return errors.New("invalid initialization once uncertainty")
	}
	for i, name := range r.UncertainOnce {
		if !members[name] || i > 0 && r.UncertainOnce[i-1] >= name {
			return errors.New("invalid initialization once uncertainty")
		}
	}
	if b.Lease != nil {
		for _, id := range b.Lease.RetainedRuns {
			held[id] = true
		}
		if b.Lease.Execution != nil {
			held[b.Lease.Execution.ID] = true
		}
	}
	for i, id := range r.RetiredRuns {
		if !identity.MatchString(id) || i > 0 && r.RetiredRuns[i-1] >= id || held[id] {
			return errors.New("invalid initialization retired history")
		}
	}
	return nil
}

// ValidateSchema19 rejects repair authority unknown to prior state versions.
func ValidateSchema19(b *model.Bench) error {
	if err := ValidateSchema20(b); err != nil {
		return err
	}
	if b.Workspace.InitializationRepair != nil {
		return errors.New("initialization repair requires current state schema")
	}
	return nil
}

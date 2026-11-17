package prepproof

import (
	"database/sql"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// Verify accepts a member preparation proof only when its run is an owned,
// sealed, successful and unreleased outcome. A proof without a run needs none.
func Verify(tx *sql.Tx, b *model.Bench, p *model.WorkspacePreparation) error {
	if p.RunID == "" {
		return nil
	}
	owned := false
	for _, id := range b.Lease.RetainedRuns {
		owned = owned || id == p.RunID
	}
	if e := b.Lease.Execution; e != nil && e.Phase == "stopped" && e.ID == p.RunID {
		owned = true
	}
	r, err := history.Get(tx, p.RunID)
	if err != nil {
		return err
	}
	if !owned || r.Key != b.Key || r.File == nil || r.ExitCode == nil || *r.ExitCode != 0 || !r.ReleasedAt.IsZero() {
		return errors.New("member preparation proof requires an owned sealed successful run")
	}
	return nil
}

// ReleaseHistory is called inside the final readiness transaction, after
// independent checkpoint and fencing verification. Any failure rolls back all
// releases along with the proposed lease removal.
func ReleaseHistory(tx *sql.Tx, b *model.Bench) error {
	if b == nil || b.Workspace == nil || b.Lease == nil || b.Workspace.Checkpoint == nil {
		return errors.New("held checkpointed workspace required for history release")
	}
	for i := range b.Workspace.Preparation {
		if err := Verify(tx, b, &b.Workspace.Preparation[i]); err != nil {
			return err
		}
	}
	ids := append([]string(nil), b.Lease.RetainedRuns...)
	if b.Workspace.Phase != "ready" && b.Workspace.InitializationRepair != nil {
		ids = append(ids, b.Workspace.InitializationRepair.RetiredRuns...)
	}
	if b.Lease.Execution != nil {
		ids = append(ids, b.Lease.Execution.ID)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		r, err := history.Get(tx, id)
		if err != nil {
			return err
		}
		if r.Key != b.Key || (r.Interruption == nil && (r.File == nil || r.ExitCode == nil)) || !r.ReleasedAt.IsZero() {
			return errors.New("workspace history requires an owned sealed retained outcome")
		}
		if err = history.Released(tx, id, b.Workspace.Checkpoint.Seal); err != nil {
			return err
		}
	}
	return nil
}

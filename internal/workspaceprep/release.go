package workspaceprep

import (
	"database/sql"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// ReleaseHistory is called inside the final readiness transaction, after
// independent checkpoint and fencing verification. Any failure rolls back all
// releases along with the proposed lease removal.
func ReleaseHistory(tx *sql.Tx, b *model.Bench) error {
	if b == nil || b.Workspace == nil || b.Lease == nil || b.Workspace.Checkpoint == nil {
		return errors.New("held checkpointed workspace required for history release")
	}
	for i := range b.Workspace.Preparation {
		if err := verifyProof(tx, b, &b.Workspace.Preparation[i]); err != nil {
			return err
		}
	}
	ids := append([]string(nil), b.Lease.RetainedRuns...)
	if b.Lease.Execution != nil {
		ids = append(ids, b.Lease.Execution.ID)
	}
	for _, id := range ids {
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

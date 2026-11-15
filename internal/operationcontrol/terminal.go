package operationcontrol

import (
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// RequireOwner recognizes the exact bound capability before or after its one-way
// continuation revocation. It never authorizes effects or a later owner's token.
func RequireOwner(r Reader, i Intent, b *model.Bench) error {
	found, err := Get(r, i.Target)
	if err != nil {
		return err
	}
	if found.Intent != i || found.Phase != "bound" || b == nil || b.Workspace == nil || b.Lease == nil || b.Key != i.Key || b.Workspace.ID != found.BenchID || b.Lease.Holder != found.Holder || hash(b.Lease.Token) != found.TokenHash {
		return ErrAuthority
	}
	if b.Lease.StopRequested {
		if b.Lease.Continuation != "" {
			return ErrAuthority
		}
	} else if hash(b.Lease.Continuation) != found.ContinuationHash {
		return ErrAuthority
	}
	return nil
}

// ConcludeCancellation releases only this epoch's exclusion, after its caller
// independently proves all helper barriers, Git controls and owned histories
// quiescent. It grants no capability and never releases or replaces a lease.
func ConcludeCancellation(tx *sql.Tx, i Intent, b *model.Bench) error {
	found, err := Get(tx, i.Target)
	if err != nil {
		return err
	}
	if found.Intent != i {
		return ErrAuthority
	}
	phase := "finished"
	switch found.Phase {
	case "finished", "stopped":
		ok, err := CancellationConcluded(tx, i)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		// The caller must establish the same complete proof as a first
		// conclusion; a normal finish alone never supplies this marker.
		return saveCancellationConclusion(tx, i)
	case "admitted": // No owner was bound; never mutate an unrelated existing lease.
	case "bound":
		if b != nil && b.Workspace != nil && b.Workspace.ID == found.BenchID && b.Lease == nil {
			if b.Workspace.Phase != "ready" || b.Workspace.Pending != nil || b.Workspace.Stage != nil {
				return ErrAuthority
			}
		} else {
			if err = RequireRevoked(tx, i, b); err != nil {
				return err
			}
			phase = "stopped"
		}
	default:
		return ErrAuthority
	}
	result, err := tx.Exec(`UPDATE workspace_operations SET phase=? WHERE id=? AND epoch=? AND phase=?`, phase, i.Target.ID(), i.Epoch, found.Phase)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAuthority
	}
	saved, err := Get(tx, i.Target)
	if err != nil {
		return err
	}
	if saved.Phase != phase {
		return ErrAuthority
	}
	return saveCancellationConclusion(tx, i)
}

// Runs returns only this exact operation's bounded immutable launch inventory.
// Preflight must load the complete vector before any cancellation effect.
func Runs(r Reader, i Intent) ([]Run, error) {
	found, err := Get(r, i.Target)
	if err != nil {
		return nil, err
	}
	if found.Intent != i {
		return nil, ErrAuthority
	}
	var n int
	if err = r.QueryRow(`SELECT count(*) FROM workspace_operation_runs WHERE operation_id=?`, i.Target.ID()).Scan(&n); err != nil {
		return nil, err
	}
	if n < 0 || n > MaxRuns {
		return nil, ErrAuthority
	}
	out := make([]Run, 0, n)
	for j := 0; j < n; j++ {
		var id string
		if err = r.QueryRow(`SELECT run_id FROM workspace_operation_runs WHERE operation_id=? ORDER BY run_id LIMIT 1 OFFSET ?`, i.Target.ID(), j).Scan(&id); err != nil {
			return nil, err
		}
		run, e := GetRun(r, i, id)
		if e != nil {
			return nil, e
		}
		out = append(out, run)
	}
	return out, nil
}

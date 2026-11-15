package workspacetransfer

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
)

func absent(path string) error {
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return errors.New("transfer destination has existing files or a legacy intent; preserve and reconcile")
	}
	return nil
}

// Begin journals concrete approval and permanently blocks source acquisition
// before any HEAD detaches. Cancellation never rolls this committed tombstone back.
func (m Manager) Begin(ctx context.Context, p Plan) (*Result, error) {
	raw, err := p.Bytes()
	if err != nil {
		return nil, err
	}
	if p.Root != m.Store.State || p.DataRoot != m.Store.Data || p.ObservedAt.After(time.Now().UTC()) {
		return nil, errors.New("transfer approval belongs to another storage root or a future observation")
	}
	ctx, cancel, a, err := m.admin(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	var out *Result
	err = a.Prep.Gate(p.SourceKey, func() error {
		return a.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if prior, e := reftransfer.Get(tx, p.ID); e == nil {
				j, _, e := boundRecord(rows, tx, p.ID)
				if e != nil {
					return e
				}
				if prior.Digest != p.Digest || !bytes.Equal(prior.Plan, raw) {
					return errors.New("transfer ID already binds another approval")
				}
				out = projection(j, rows[j.TargetKey], false)
				return nil
			} else if e != sql.ErrNoRows {
				return e
			}
			current, e := m.inspectState(ctx, a, rows, p)
			if e != nil {
				return e
			}
			if current.Digest != p.Digest {
				return errors.New("source files, registrations, vector or ownership changed after transfer review")
			}
			if e = a.Prep.Admit(); e != nil {
				return e
			}
			j := reftransfer.Record{ID: p.ID, SourceID: p.SourceID, SourceKey: p.SourceKey, TargetID: p.TargetID, TargetKey: p.TargetKey, CreationID: p.CreationID, Digest: p.Digest, Plan: raw, CreatedAt: time.Now().UTC(), Phase: "started"}
			if e = reftransfer.Start(tx, j); e != nil {
				return e
			}
			rows[p.SourceKey].Workspace.Transfer = &model.WorkspaceTransfer{ID: p.ID, Digest: p.Digest, TargetKey: p.TargetKey, TargetID: p.TargetID, CreationID: p.CreationID, Phase: "relinquishing"}
			out = projection(j, nil, false)
			return ctx.Err()
		})
	})
	if err != nil {
		return nil, err
	}
	return out, ctx.Err()
}

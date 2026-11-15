package workspacetransfer

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
)

// BeginMissing retires the exact approved stopped owner without releasing its
// history or claiming that lost files belong to the last confirmed checkpoint.
func (m Manager) BeginMissing(ctx context.Context, p MissingPlan) (*Result, error) {
	raw, err := p.Bytes()
	if err != nil {
		return nil, err
	}
	if p.Root != m.Store.State || p.DataRoot != m.Store.Data || p.ObservedAt.After(time.Now().UTC()) {
		return nil, errors.New("missing-source approval belongs to another root or future observation")
	}
	ctx, cancel, a, err := m.admin(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	var out *Result
	var locks transferCaptures
	err = a.Prep.Gate(p.SourceKey, func() error {
		defer func() { locks.close() }()
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
			current, e := m.inspectMissingState(ctx, a, rows, tx, p, &locks)
			if e != nil {
				return e
			}
			if current.Digest != p.Digest {
				return errors.New("missing-source ownership, evidence, files or storage changed after review")
			}
			if e = a.Prep.Admit(); e != nil {
				return e
			}
			if e = locks.check(m.Store.State); e != nil {
				return e
			}
			j := reftransfer.Record{ID: p.ID, SourceID: p.SourceID, SourceKey: p.SourceKey, TargetID: p.TargetID, TargetKey: p.TargetKey, CreationID: p.CreationID, Digest: p.Digest, Plan: raw, CreatedAt: time.Now().UTC(), Phase: "started"}
			if e = reftransfer.Start(tx, j); e != nil {
				return e
			}
			b := rows[p.SourceKey]
			r := p.Recovery
			b.Workspace.Transfer = &model.WorkspaceTransfer{ID: p.ID, Digest: p.Digest, TargetKey: p.TargetKey, TargetID: p.TargetID, CreationID: p.CreationID, Phase: "relinquishing", Recovery: &r}
			b.Lease, b.Workspace.Stage = nil, nil
			out = projection(j, nil, false)
			return ctx.Err()
		})
	})
	if err != nil {
		return nil, err
	}
	return out, ctx.Err()
}

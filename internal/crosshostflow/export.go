package crosshostflow

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffguard"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hostidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

func inspect(ctx context.Context, a workspacerepair.Manager, rows benchstate.Database, tx *sql.Tx, p Plan) (Plan, error) {
	b := rows[p.SourceKey]
	if err := workspacepolicy.Validate(b); err != nil {
		return p, err
	}
	w := b.Workspace
	if w.Phase != "ready" || w.Transfer != nil || w.Archive != nil || b.Lease != nil || w.Stage != nil || w.Pending != nil || w.Checkpoint == nil {
		return p, errors.New("export requires an unleased ready checkpointed workspace")
	}
	host, err := hostidentity.Current(ctx)
	if err != nil {
		return p, err
	}
	state, err := handoffjournal.StateID(tx)
	if err != nil {
		return p, err
	}
	if err = a.Lifecycle.Stopped(workspace.Clone(b)); err != nil {
		return p, err
	}
	vector, err := a.Lifecycle.Inspect(ctx, b, true)
	if err != nil {
		return p, err
	}
	if !reflect.DeepEqual(vector, w.Checkpoint.Members) {
		return p, errors.New("export requires exact checkpointed HEAD vector")
	}
	if err = a.Lifecycle.Confirm(ctx, b); err != nil {
		return p, err
	}
	p.SourceID = w.ID
	p.SourceHost = host
	p.SourceState = state
	p.SourceRowDigest = hash(b)
	p.Checkpoint = workspace.Clone(b).Workspace.Checkpoint
	p.CheckpointRepository = w.CheckpointRepository
	p.Members = nil
	worker := worktreetransfer.Manager{Git: a.Prep.Git, Timeout: a.Lifecycle.Timeout}
	for n, r := range w.Members {
		selected, err := worker.Inspect(ctx, r.Path, r.Mirror, "refs/heads/"+r.Branch, vector[n].Commit)
		if err != nil {
			return p, err
		}
		p.Members = append(p.Members, Member{Name: r.Name, Repository: r.Repository, Branch: r.Branch, IntegrationBase: r.Base, Worktree: selected})
	}
	p.Digest = ""
	p.Digest = hash(p)
	return p, p.valid()
}
func (m Manager) InspectExport(ctx context.Context, source, id string, offer handoffjournal.Offer) (*Plan, error) {
	if !key.MatchString(source) || !key.MatchString(id) {
		return nil, errors.New("valid source and export identity required")
	}
	if _, err := OfferBytes(offer); err != nil {
		return nil, err
	}
	ctx, cancel, a, err := m.admin(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	p := Plan{Version: Version, Root: m.Store.State, DataRoot: m.Store.Data, ID: id, SourceKey: source, Destination: offer, ObservedAt: time.Now().UTC()}
	err = a.Prep.Gate(source, func() error {
		return a.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			var e error
			p, e = inspect(ctx, a, rows, tx, p)
			return e
		})
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// BeginExport commits the exact reviewed vector and permanent source exclusion
// together. No detachment occurs until that transaction is durably successful.
func (m Manager) BeginExport(ctx context.Context, p Plan) (*Result, error) {
	raw, err := p.Bytes()
	if err != nil {
		return nil, err
	}
	if p.Root != m.Store.State || p.DataRoot != m.Store.Data || p.ObservedAt.After(time.Now().UTC()) {
		return nil, errors.New("export approval belongs to another root or future observation")
	}
	artifact, err := p.Artifact()
	if err != nil {
		return nil, err
	}
	ctx, cancel, a, err := m.admin(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	var out *Result
	err = a.Prep.Gate(p.SourceKey, func() error {
		return a.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			if e, err := handoffjournal.ExportByID(tx, p.ID); err == nil {
				if !bytes.Equal(e.Approval, raw) || !reflect.DeepEqual(e.Artifact, artifact) {
					return handoffjournal.ErrAuthority
				}
				b := rows[p.SourceKey]
				if b == nil || b.Workspace == nil || b.Workspace.Transfer == nil || b.Workspace.Transfer.CrossHost == nil {
					return handoffjournal.ErrAuthority
				}
				if err = handoffguard.Current(ctx, tx, b); err != nil {
					return err
				}
				out = result(nil, p.ID, e.Phase, false)
				return ctx.Err()
			} else if err != sql.ErrNoRows {
				return err
			}
			current, err := inspect(ctx, a, rows, tx, p)
			if err != nil {
				return err
			}
			if current.Digest != p.Digest {
				return errors.New("export approval became stale")
			}
			if err = handoffjournal.StartExport(tx, handoffjournal.Export{Artifact: artifact, Approval: raw, Phase: "started"}); err != nil {
				return err
			}
			d := artifact.Destination
			rows[p.SourceKey].Workspace.Transfer = &model.WorkspaceTransfer{ID: p.ID, Digest: artifact.Source.ApprovalDigest, TargetKey: d.Key, TargetID: d.WorkspaceID, CreationID: d.CreationID, Phase: "relinquishing", CrossHost: &model.WorkspaceHandoff{HostID: d.HostID, StateID: d.StateID, ArtifactDigest: artifact.Digest}}
			out = result(nil, p.ID, "started", false)
			return ctx.Err()
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func boundExport(ctx context.Context, rows benchstate.Database, tx *sql.Tx, source, id string) (handoffjournal.Export, Plan, error) {
	e, err := handoffjournal.ExportByID(tx, id)
	if err != nil {
		return e, Plan{}, err
	}
	p, err := ParsePlan(e.Approval)
	if err != nil {
		return e, p, err
	}
	selected, err := p.Artifact()
	if err != nil || !reflect.DeepEqual(selected, e.Artifact) || source != p.SourceKey || source != e.Artifact.Source.Key {
		return e, p, handoffjournal.ErrAuthority
	}
	b := rows[source]
	if b == nil || b.Workspace == nil || b.Workspace.Transfer == nil || b.Workspace.Transfer.CrossHost == nil {
		return e, p, handoffjournal.ErrAuthority
	}
	return e, p, handoffguard.Current(ctx, tx, b)
}

// ContinueExport publishes an artifact only after every source registration is
// detached and the immutable full-vector checkpoint is independently confirmed.
func (m Manager) ContinueExport(ctx context.Context, source, id string) (*crosshost.Artifact, error) {
	if !key.MatchString(source) || !key.MatchString(id) {
		return nil, errors.New("valid source/export identity required")
	}
	ctx, cancel, a, err := m.admin(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	var out *crosshost.Artifact
	err = a.Prep.Gate(source, func() error {
		return a.Lifecycle.Within(func(update workspace.Update) error {
			var e handoffjournal.Export
			var p Plan
			guard := func() error {
				return update(func(rows benchstate.Database, tx *sql.Tx) error {
					var err error
					e, p, err = boundExport(ctx, rows, tx, source, id)
					if err != nil {
						return err
					}
					if p.Root != m.Store.State || p.DataRoot != m.Store.Data {
						return handoffjournal.ErrAuthority
					}
					return ctx.Err()
				})
			}
			if err := guard(); err != nil {
				return err
			}
			if e.Phase == "relinquished" {
				out = &e.Artifact
				return nil
			}
			worker := worktreetransfer.Manager{Git: a.Prep.Git, Timeout: a.Lifecycle.Timeout, Gate: func(context.Context, worktreetransfer.Plan) error { return guard() }}
			// Inspect the complete mixed attached/detached retry vector before any effect.
			for _, m := range p.Members {
				w := m.Worktree
				current, err := worker.Inspect(ctx, w.Checkout, w.Common, w.TaskRef, w.Commit)
				if err != nil {
					current, err = worker.InspectDetached(ctx, w.Checkout, w.Common, w.TaskRef, w.Commit)
				}
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(current, w) {
					return errors.New("source tree or administrative evidence changed")
				}
			}
			for n := 0; n < 2; n++ {
				for _, m := range p.Members {
					if err := worker.Apply(ctx, m.Worktree); err != nil {
						return err
					}
				}
			}
			return update(func(rows benchstate.Database, tx *sql.Tx) error {
				e, _, err := boundExport(ctx, rows, tx, source, id)
				if err != nil {
					return err
				}
				if e.Phase != "started" {
					return handoffjournal.ErrAuthority
				}
				if err = a.Lifecycle.Confirm(ctx, rows[source]); err != nil {
					return err
				}
				if err = handoffjournal.RelinquishExport(tx, id, e.Artifact.Digest); err != nil {
					return err
				}
				rows[source].Workspace.Transfer.Phase = "transferred"
				out = &e.Artifact
				return ctx.Err()
			})
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

package workspacerepair

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/initrepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/transferguard"
)

func (m Manager) Claim(ctx context.Context, p Plan, id string) (out *Result, operationErr error) {
	ctx, err := m.controlled(ctx)
	if err != nil {
		return nil, err
	}
	if !requestID.MatchString(id) || id == p.PreviousID {
		return nil, errors.New("a fresh initialization repair ID is required")
	}
	raw, err := p.Bytes()
	if err != nil {
		return nil, err
	}
	if p.Root != m.Prep.State || p.DataRoot != m.Prep.Data || p.ObservedAt.After(time.Now().UTC()) {
		return nil, errors.New("initialization repair plan belongs to another storage root or future observation")
	}
	var token string
	var locks captureLocks

	// Revoke only the exact owner authenticated or committed below. Reconciliation
	// runs after leaving the keyed gate and SQL callback, including replay errors.
	defer func() {
		if token == "" {
			return
		}
		if ctxErr := ctx.Err(); ctxErr != nil || errors.Is(operationErr, transferguard.ErrGuard) {
			_, stopErr := m.Prep.Stop(p.Key, token, false)
			current, readErr := m.view(p.Key, id, token, false)
			out = current
			operationErr = errors.Join(operationErr, ctxErr, stopErr, readErr)
		}
	}()
	err = m.Prep.Gate(p.Key, func() error {
		defer func() { locks.close() }()
		return m.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := operationcontrol.GuardContext(ctx, tx, rows[p.Key]); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if prior, e := initrepair.Get(tx, p.BenchID, id); e == nil {
				if prior.Key != p.Key || prior.Digest != p.Digest || !bytes.Equal(prior.Plan, raw) {
					return errors.New("initialization repair ID is bound to different approval")
				}
				b := rows[p.Key]
				if b == nil || b.Workspace == nil || b.Workspace.ID != p.BenchID || b.Workspace.InitializationRepair == nil || b.Workspace.InitializationRepair.ID != id || b.Workspace.InitializationRepair.Digest != p.Digest {
					return errors.New("initialization repair was replaced")
				}
				if prior.Status == "completed" && b.Workspace.Phase == "ready" && b.Lease == nil {
					return nil
				}
				if prior.Status != "active" || b.Lease == nil || b.Lease.StopRequested || b.Lease.Continuation == "" || b.Lease.Holder != "initialization" || b.Workspace.Phase == "ready" || hashBytes([]byte(b.Lease.Token)) != prior.OwnerDigest {
					return errors.New("initialization repair is stopped or terminal; inspect a new repair")
				}
				token = b.Lease.Token
				return transferguard.Check(ctx, transferguard.Config{State: m.Prep.State, Data: m.Prep.Data, Git: m.Prep.Git}, tx, b)
			} else if e != sql.ErrNoRows {
				return e
			}
			var choices []string
			for _, member := range p.Members {
				if member.Action == "rerun-once" {
					choices = append(choices, member.Name)
				}
			}
			current, err := m.inspectState(ctx, rows, tx, p.Key, choices, p.ObservedAt, &locks)
			if err != nil {
				return err
			}
			if current.Digest != p.Digest {
				return errors.New("initialization state or evidence changed after approval")
			}
			if err = m.Prep.Admit(); err != nil {
				return err
			}
			b := rows[p.Key]
			previousExecution := b.Lease.Execution
			allRuns := map[string]bool{}
			uncertain := map[string]bool{}
			for _, h := range p.History {
				allRuns[h.ID] = true
			}
			if old := b.Workspace.InitializationRepair; old != nil {
				for _, name := range old.UncertainOnce {
					uncertain[name] = true
				}
			}
			for _, member := range p.Members {
				if member.Action == "rerun-once" {
					uncertain[member.Name] = true
				}
			}
			if err = m.Prep.ApplyInitializationRepair(ctx, b, tx, p.Members); err != nil {
				return err
			}
			dependencies := map[string]bool{}
			for _, proof := range b.Workspace.Preparation {
				if proof.RunID != "" {
					dependencies[proof.RunID] = true
				}
			}
			owner, err := model.NewLease("initialization")
			if err != nil {
				return err
			}
			owner.Continuation, err = model.Nonce()
			if err != nil {
				return err
			}
			marker := &model.WorkspaceInitializationRepair{ID: id, Digest: p.Digest, PreviousID: p.PreviousID}
			for run := range dependencies {
				if !allRuns[run] {
					return errors.New("initialization proof is not retained owned history")
				}
				owner.RetainedRuns = append(owner.RetainedRuns, run)
			}
			sort.Strings(owner.RetainedRuns)
			for run := range allRuns {
				if !dependencies[run] {
					marker.RetiredRuns = append(marker.RetiredRuns, run)
				}
			}
			for name := range uncertain {
				marker.UncertainOnce = append(marker.UncertainOnce, name)
			}
			sort.Strings(marker.RetiredRuns)
			sort.Strings(marker.UncertainOnce)
			if len(owner.RetainedRuns) > model.WorkspaceRetainedRuns {
				return errors.New("initialization proof dependencies exceed retained budget")
			}
			if p.PreviousID != "" {
				old := b.Workspace.InitializationRepair
				if err = initrepair.Supersede(tx, p.BenchID, p.PreviousID, old.Digest); err != nil {
					return err
				}
			}
			journal := initrepair.Record{BenchID: p.BenchID, ID: id, Key: p.Key, Digest: p.Digest, PreviousID: p.PreviousID, OwnerDigest: hashBytes([]byte(owner.Token)), Plan: raw, CreatedAt: time.Now().UTC(), Status: "active"}
			if err = initrepair.Start(tx, journal); err != nil {
				return err
			}
			b.Lease = owner
			b.Workspace.InitializationRepair = marker
			b.Workspace.Stage = nil
			if b.Workspace.Pending == nil && len(b.Workspace.Preparation) != len(b.Workspace.Members) {
				b.Workspace.Phase = "initializing"
			}
			if previousExecution != nil {
				if err = m.Prep.Backend.Empty(previousExecution); err != nil {
					return err
				}
			}
			if err = locks.check(m.Prep.State); err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			token = owner.Token
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	// A source may have changed while claim committed or its prior reply was lost.
	// Recheck before exporting a fresh capability; completed metadata replay does
	// not depend on a source allocation that may already have been retired.
	if token != "" {
		if err = m.Prep.Gate(p.Key, func() error {
			return m.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
				b := rows[p.Key]
				if b == nil || b.Lease == nil || b.Lease.Token != token {
					return errors.New("initialization repair owner changed before grant")
				}
				return transferguard.Check(ctx, transferguard.Config{State: m.Prep.State, Data: m.Prep.Data, Git: m.Prep.Git}, tx, b)
			})
		}); err != nil {
			return nil, err
		}
	}
	return m.view(p.Key, id, token, true)
}

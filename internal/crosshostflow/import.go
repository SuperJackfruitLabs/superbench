package crosshostflow

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffguard"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hostidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

// Import accepts only explicitly reviewed final export bytes for this existing
// actual-host offer. The administrator must independently review source retirement;
// a forged digest or arbitrary file is not an attestation of a remote machine.
func (m Manager) Import(ctx context.Context, a crosshost.Artifact) (*Result, error) {
	if err := a.RequireDestination(a.Destination); err != nil {
		return nil, err
	}
	ctx, cancel, admin, err := m.admin(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	err = admin.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		id, err := hostidentity.Current(ctx)
		if err != nil {
			return err
		}
		if id != a.Destination.HostID {
			return handoffjournal.ErrAuthority
		}
		if rows[a.Destination.Key] != nil {
			prior, err := handoffjournal.ImportByID(tx, a.Destination.CreationID)
			if err != nil || !reflect.DeepEqual(prior.Artifact, a) {
				return handoffjournal.ErrAuthority
			}
		}
		if err = handoffjournal.AcceptImport(tx, a); err != nil {
			return err
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return m.ContinueImport(ctx, a.Destination.Key, a.Destination.CreationID)
}

func incoming(ctx context.Context, rows benchstate.Database, tx *sql.Tx, key, creationID string) (handoffjournal.Import, *model.Bench, error) {
	i, err := handoffjournal.ImportByID(tx, creationID)
	if err != nil {
		return i, nil, err
	}
	if i.Artifact.Destination.Key != key {
		return i, nil, handoffjournal.ErrAuthority
	}
	host, err := hostidentity.Current(ctx)
	if err != nil {
		return i, nil, err
	}
	if host != i.Artifact.Destination.HostID {
		return i, nil, handoffjournal.ErrAuthority
	}
	b := rows[key]
	if b != nil {
		if b.Workspace == nil || b.Workspace.ID != i.Artifact.Destination.WorkspaceID || b.Workspace.CreationID != creationID || b.Workspace.TransferredFrom == nil || b.Workspace.TransferredFrom.CrossHost == nil {
			return i, nil, handoffjournal.ErrAuthority
		}
		if err = handoffguard.Current(ctx, tx, b); err != nil {
			return i, nil, err
		}
	}
	return i, workspace.Clone(b), ctx.Err()
}

// ContinueImport never selects a new identity, revives a stopped initialization
// owner or exposes a later operational/repair capability on historical replay.
func (m Manager) ContinueImport(ctx context.Context, keyName, creationID string) (out *Result, operationErr error) {
	if !key.MatchString(keyName) || !key.MatchString(creationID) {
		return nil, errors.New("valid import key and creation identity required")
	}
	ctx, cancel, admin, err := m.admin(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	var current *model.Bench
	var i handoffjournal.Import
	var token string
	authorized := false
	defer func() {
		if !authorized || i.Phase == "completed" {
			return
		}
		read := func(grant bool) error {
			return admin.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
				latest, b, e := incoming(context.WithoutCancel(ctx), rows, tx, keyName, creationID)
				if e != nil {
					return e
				}
				out = importResult(latest, b, grant)
				if token == "" {
					token = importResult(latest, b, true).GrantToken
				}
				return nil
			})
		}
		operationErr = errors.Join(operationErr, read(ctx.Err() == nil))
		if ctx.Err() != nil {
			operationErr = errors.Join(operationErr, ctx.Err())
			if token != "" {
				_, e := admin.Prep.Stop(keyName, token, false)
				operationErr = errors.Join(operationErr, e)
			}
			operationErr = errors.Join(operationErr, read(false))
		}
	}()
	err = admin.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		var e error
		i, current, e = incoming(context.WithoutCancel(ctx), rows, tx, keyName, creationID)
		if e != nil {
			return e
		}
		authorized = true
		token = importResult(i, current, true).GrantToken
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	if i.Phase == "completed" {
		return importResult(i, current, false), ctx.Err()
	}
	admin.Prep.TransferID = i.Artifact.Source.TransferID
	admin.Lifecycle.TransferID = admin.Prep.TransferID
	stage := workspace.Manager{State: m.Store.State, Data: m.Store.Data, Timeout: admin.Lifecycle.Timeout, Within: admin.Lifecycle.Within, Git: admin.Lifecycle.Git, Admit: admin.Lifecycle.Admit, Allocate: admin.Lifecycle.Allocate}
	current, err = stage.StageHandoff(ctx, i.Artifact)
	if current != nil {
		token = result(current, i.Artifact.Source.TransferID, "accepted", true).GrantToken
	}
	if err != nil {
		return nil, err
	}
	if current.Workspace.Phase != "ready" {
		if current.Workspace.InitializationRepair != nil || current.Lease == nil || token == "" || current.Lease.StopRequested {
			return nil, errors.New("import initialization is stopped; use its explicit repair")
		}
		if current.Workspace.Pending == nil {
			if _, err = admin.Prep.Prepare(ctx, keyName, token, "", false); err != nil {
				return nil, err
			}
		}
		if _, err = admin.Lifecycle.FinishInitialization(ctx, keyName, token); err != nil {
			return nil, err
		}
	}
	// Core readiness already completed the import journal in the same transaction.
	return nil, nil
}

func importResult(i handoffjournal.Import, b *model.Bench, grant bool) *Result {
	r := result(b, i.Artifact.Source.TransferID, i.Phase, grant)
	if b == nil {
		d := i.Artifact.Destination
		r.Key = d.Key
		r.WorkspaceID = d.WorkspaceID
		r.CreationID = d.CreationID
	}
	return r
}

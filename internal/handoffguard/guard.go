// Package handoffguard binds core rows to cross-host journals without granting
// source-fencing authority to an artifact or its integrity digest.
package handoffguard

import (
	"context"
	"database/sql"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hostidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

var ErrAuthority = errors.New("cross-host core identity differs from retained handoff")

// Record is checked in the core transaction, including ordinary registration.
func Record(db handoffjournal.Reader, b *model.Bench) error {
	offer, err := handoffjournal.OfferByKey(db, b.Key)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && (b.Workspace == nil || b.Workspace.TransferredFrom == nil || b.Workspace.TransferredFrom.CrossHost == nil || b.Workspace.CreationID != offer.Destination.CreationID) {
		return ErrAuthority
	}
	if b.Workspace == nil {
		return nil
	}
	w := b.Workspace
	if t := w.Transfer; t != nil && t.CrossHost != nil {
		e, err := handoffjournal.ExportByID(db, t.ID)
		if err != nil {
			return err
		}
		a := e.Artifact
		if a.Source.Key != b.Key || a.Source.WorkspaceID != w.ID || a.Source.ApprovalDigest != t.Digest || a.Digest != t.CrossHost.ArtifactDigest || a.Destination.HostID != t.CrossHost.HostID || a.Destination.StateID != t.CrossHost.StateID || a.Destination.Key != t.TargetKey || a.Destination.WorkspaceID != t.TargetID || a.Destination.CreationID != t.CreationID || (e.Phase == "started") != (t.Phase == "relinquishing") {
			return ErrAuthority
		}
		if w.CheckpointRepository != a.CheckpointRepository || w.Checkpoint == nil || a.Seal != w.Checkpoint.Seal || a.ManifestDigest != w.Checkpoint.Digest || !members(a, b, false) {
			return ErrAuthority
		}
	}
	if o := w.TransferredFrom; o != nil && o.CrossHost != nil {
		i, err := handoffjournal.ImportByID(db, w.CreationID)
		if err != nil {
			return err
		}
		a := i.Artifact
		if a.Source.TransferID != o.ID || a.Source.ApprovalDigest != o.Digest || a.Source.Key != o.SourceKey || a.Source.WorkspaceID != o.SourceID || a.Source.HostID != o.CrossHost.HostID || a.Source.StateID != o.CrossHost.StateID || a.Digest != o.CrossHost.ArtifactDigest || a.Destination.Key != b.Key || a.Destination.WorkspaceID != w.ID || a.CheckpointRepository != w.CheckpointRepository || offer.Destination != a.Destination || offer.QuotaBytes != w.ExplicitQuotaBytes || !members(a, b, true) {
			return ErrAuthority
		}
		var repo string
		for _, m := range a.Members {
			if m.Name == a.CheckpointRepository {
				repo = m.Repository
			}
		}
		r := w.RecoveredFrom
		if r == nil || r.Repository != repo || r.BenchID != a.Source.WorkspaceID || r.Commit != a.Seal || r.ManifestSHA256 != a.ManifestDigest {
			return ErrAuthority
		}
		if i.Phase == "completed" && (w.Phase == "materializing" || w.Phase == "initializing" || w.Phase == "prepared") {
			return ErrAuthority
		}
	}
	return nil
}

func members(a crosshost.Artifact, b *model.Bench, incoming bool) bool {
	w := b.Workspace
	if len(a.Members) != len(w.Members) {
		return false
	}
	for n, m := range a.Members {
		r := w.Members[n]
		if m.Name != r.Name || m.Repository != r.Repository || m.Branch != r.Branch || m.IntegrationBase != r.Base {
			return false
		}
		if incoming && m.Commit != r.Source {
			return false
		}
		if !incoming && (w.Checkpoint == nil || len(w.Checkpoint.Members) != len(a.Members) || w.Checkpoint.Members[n].Name != m.Name || w.Checkpoint.Members[n].Commit != m.Commit) {
			return false
		}
	}
	return true
}

// Current additionally observes this actual machine. It must run before every
// imported initialization effect; a reflected caller label is not this check.
func Current(ctx context.Context, db handoffjournal.Reader, b *model.Bench) error {
	if err := Record(db, b); err != nil {
		return err
	}
	id, err := hostidentity.Current(ctx)
	if err != nil {
		return err
	}
	var expected string
	if b.Workspace.Transfer != nil && b.Workspace.Transfer.CrossHost != nil {
		e, err := handoffjournal.ExportByID(db, b.Workspace.Transfer.ID)
		if err != nil {
			return err
		}
		expected = e.Artifact.Source.HostID
	} else if b.Workspace.TransferredFrom != nil && b.Workspace.TransferredFrom.CrossHost != nil {
		i, err := handoffjournal.ImportByID(db, b.Workspace.CreationID)
		if err != nil {
			return err
		}
		expected = i.Artifact.Destination.HostID
	}
	if expected == "" || expected != id {
		return ErrAuthority
	}
	return ctx.Err()
}

// Advance shares the actual ready/unleased core transition, including explicit
// repair completion. The lifecycle remains responsible for remote/writer proof.
func Advance(tx *sql.Tx, b *model.Bench) error {
	if err := Record(tx, b); err != nil {
		return err
	}
	if b.Workspace == nil || b.Workspace.TransferredFrom == nil || b.Workspace.TransferredFrom.CrossHost == nil || b.Workspace.Phase != "ready" || b.Lease != nil {
		return nil
	}
	i, err := handoffjournal.ImportByID(tx, b.Workspace.CreationID)
	if err != nil {
		return err
	}
	return handoffjournal.CompleteImport(tx, b.Workspace.CreationID, i.Artifact.Digest)
}

package handoffjournal

import (
	"bytes"
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
)

type Import struct {
	Artifact crosshost.Artifact
	Phase    string
}

func ImportByID(db Reader, creationID string) (Import, error) {
	var i Import
	var host, state, workspaceID, transferID string
	var raw []byte
	err := db.QueryRow(`SELECT source_host,source_state,source_id,transfer_id,CASE WHEN length(artifact)<=? THEN artifact ELSE NULL END,phase FROM workspace_handoff_imports WHERE creation_id=?`, crosshost.MaxArtifact, creationID).Scan(&host, &state, &workspaceID, &transferID, &raw, &i.Phase)
	if err != nil {
		return i, err
	}
	i.Artifact, err = crosshost.Parse(raw)
	if err != nil || i.Phase != "accepted" && i.Phase != "completed" {
		return Import{}, ErrAuthority
	}
	s := i.Artifact.Source
	if s.HostID != host || s.StateID != state || s.WorkspaceID != workspaceID || s.TransferID != transferID || i.Artifact.Destination.CreationID != creationID {
		return Import{}, ErrAuthority
	}
	offer, err := OfferByID(db, creationID)
	if err != nil || i.Artifact.RequireDestination(offer.Destination) != nil {
		return Import{}, ErrAuthority
	}
	return i, nil
}

// AcceptImport retains the exact reviewed export against the existing offer
// before allocation or checkout. The coordinator must independently verify
// source retirement and actual local host identity; parsing is not that proof.
func AcceptImport(tx *sql.Tx, artifact crosshost.Artifact) error {
	raw, err := crosshost.Seal(artifact)
	if err != nil {
		return err
	}
	d := artifact.Destination
	offer, err := OfferByID(tx, d.CreationID)
	if err != nil || artifact.RequireDestination(offer.Destination) != nil {
		return ErrAuthority
	}
	prior, err := ImportByID(tx, d.CreationID)
	if err == nil {
		priorRaw, err := crosshost.Seal(prior.Artifact)
		if err != nil || !bytes.Equal(raw, priorRaw) {
			return ErrAuthority
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	if err = budget(tx, "workspace_handoff_imports"); err != nil {
		return err
	}
	s := artifact.Source
	r, err := tx.Exec(`INSERT INTO workspace_handoff_imports(creation_id,source_host,source_state,source_id,transfer_id,artifact,phase) VALUES(?,?,?,?,?,?,'accepted')`, d.CreationID, s.HostID, s.StateID, s.WorkspaceID, s.TransferID, raw)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return ErrAuthority
	}
	got, err := ImportByID(tx, d.CreationID)
	if err != nil || got.Phase != "accepted" || got.Artifact.Digest != artifact.Digest {
		return ErrAuthority
	}
	return nil
}

// CompleteImport follows independently confirmed setup and full-vector genesis
// and shares that ready transition's core transaction. No import can reset.
func CompleteImport(tx *sql.Tx, creationID, artifactDigest string) error {
	i, err := ImportByID(tx, creationID)
	if err != nil {
		return err
	}
	if i.Artifact.Digest != artifactDigest {
		return ErrAuthority
	}
	if i.Phase == "completed" {
		return nil
	}
	r, err := tx.Exec(`UPDATE workspace_handoff_imports SET phase='completed' WHERE creation_id=? AND phase='accepted'`, creationID)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return ErrAuthority
	}
	got, err := ImportByID(tx, creationID)
	if err != nil || got.Phase != "completed" || got.Artifact.Digest != artifactDigest {
		return ErrAuthority
	}
	return nil
}

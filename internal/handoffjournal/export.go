package handoffjournal

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
)

type Export struct {
	Artifact crosshost.Artifact
	Approval []byte
	Phase    string
}

func (e Export) valid() bool {
	if len(e.Approval) == 0 || len(e.Approval) > MaxApproval || !json.Valid(e.Approval) || e.Phase != "started" && e.Phase != "relinquished" {
		return false
	}
	sum := sha256.Sum256(e.Approval)
	return e.Artifact.Source.ApprovalDigest == hex.EncodeToString(sum[:])
}

func ExportByID(db Reader, id string) (Export, error) {
	var e Export
	var sourceID, sourceKey string
	var raw []byte
	err := db.QueryRow(`SELECT source_id,source_key,CASE WHEN length(artifact)<=? THEN artifact ELSE NULL END,CASE WHEN length(approval)<=? THEN approval ELSE NULL END,phase FROM workspace_handoff_exports WHERE transfer_id=?`, crosshost.MaxArtifact, MaxApproval, id).Scan(&sourceID, &sourceKey, &raw, &e.Approval, &e.Phase)
	if err != nil {
		return e, err
	}
	e.Artifact, err = crosshost.Parse(raw)
	if err != nil || !e.valid() || e.Artifact.Source.TransferID != id || e.Artifact.Source.WorkspaceID != sourceID || e.Artifact.Source.Key != sourceKey {
		return Export{}, ErrAuthority
	}
	state, err := StateID(db)
	if err != nil || state != e.Artifact.Source.StateID {
		return Export{}, ErrAuthority
	}
	return e, nil
}

// StartExport must share the core transaction that permanently excludes source
// launches. The caller has to inspect source ownership and the actual whole
// vector before that transaction. This record alone never proves retirement.
func StartExport(tx *sql.Tx, e Export) error {
	if err := e.Artifact.RequireDestination(e.Artifact.Destination); err != nil {
		return err
	}
	raw, err := crosshost.Seal(e.Artifact)
	if err != nil || !e.valid() || e.Phase != "started" {
		return ErrAuthority
	}
	state, err := StateID(tx)
	if err != nil || state != e.Artifact.Source.StateID {
		return ErrAuthority
	}
	prior, err := ExportByID(tx, e.Artifact.Source.TransferID)
	if err == nil {
		priorRaw, err := crosshost.Seal(prior.Artifact)
		if err != nil || !bytes.Equal(priorRaw, raw) || !bytes.Equal(prior.Approval, e.Approval) {
			return ErrAuthority
		}
		// Lost replies replay the same binding even after retirement; never reset.
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	if err = budget(tx, "workspace_handoff_exports"); err != nil {
		return err
	}
	s := e.Artifact.Source
	r, err := tx.Exec(`INSERT INTO workspace_handoff_exports(transfer_id,source_id,source_key,artifact,approval,phase) VALUES(?,?,?,?,?,'started')`, s.TransferID, s.WorkspaceID, s.Key, raw, e.Approval)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return ErrAuthority
	}
	got, err := ExportByID(tx, s.TransferID)
	if err != nil || got.Phase != "started" || got.Artifact.Digest != e.Artifact.Digest || !bytes.Equal(got.Approval, e.Approval) {
		return ErrAuthority
	}
	return nil
}

// RelinquishExport follows actual whole-vector writer-stop and ref detachment
// verification. It runs atomically with the permanent source marker's advance.
func RelinquishExport(tx *sql.Tx, id, artifactDigest string) error {
	e, err := ExportByID(tx, id)
	if err != nil {
		return err
	}
	if e.Artifact.Digest != artifactDigest {
		return ErrAuthority
	}
	if e.Phase == "relinquished" {
		return nil
	}
	r, err := tx.Exec(`UPDATE workspace_handoff_exports SET phase='relinquished' WHERE transfer_id=? AND phase='started'`, id)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return ErrAuthority
	}
	got, err := ExportByID(tx, id)
	if err != nil || got.Phase != "relinquished" || got.Artifact.Digest != artifactDigest {
		return ErrAuthority
	}
	return nil
}

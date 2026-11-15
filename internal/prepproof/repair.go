package prepproof

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"time"
)

type repairReceipt struct {
	Key, Digest, RunID, Completed string
	Present                       bool
}

func ownedRun(b *model.Bench, id string) bool {
	if b.Lease.Execution != nil && b.Lease.Execution.ID == id {
		return true
	}
	ids := append([]string(nil), b.Lease.RetainedRuns...)
	if marker := b.Workspace.InitializationRepair; marker != nil {
		ids = append(ids, marker.RetiredRuns...)
	}
	for _, retained := range ids {
		if retained == id {
			return true
		}
	}
	return false
}

func Success(tx *sql.Tx, b *model.Bench, id string) bool {
	if id == "" || !ownedRun(b, id) {
		return false
	}
	r, err := history.Get(tx, id)
	return err == nil && r.Key == b.Key && r.Holder == b.Lease.Holder && r.Origin == "supervised" && r.Interruption == nil && r.File != nil && !r.SealedAt.IsZero() && r.ExitCode != nil && *r.ExitCode == 0 && r.ReleasedAt.IsZero()
}

func ReceiptState(tx *sql.Tx, b *model.Bench, member, digest string) (string, bool, error) {
	r := repairReceipt{Key: "workspace:" + b.Workspace.ID + ":" + member}
	err := tx.QueryRow("SELECT digest,run_id,completed FROM setup_receipts WHERE bench=?", r.Key).Scan(&r.Digest, &r.RunID, &r.Completed)
	if err != nil && err != sql.ErrNoRows {
		return "", false, err
	}
	r.Present = err == nil
	raw, err := json.Marshal(r)
	if err != nil {
		return "", false, err
	}
	sum := sha256.Sum256(raw)
	completed, parseErr := time.Parse(time.RFC3339Nano, r.Completed)
	valid := r.Present && r.Digest == digest && parseErr == nil && !completed.IsZero() && !completed.After(time.Now()) && Success(tx, b, r.RunID)
	return hex.EncodeToString(sum[:]), valid, nil
}

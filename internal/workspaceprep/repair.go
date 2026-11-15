package workspaceprep

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

// RepairMember records decisions only; no command, capability or raw receipt is
// exposed. ReceiptDigest binds even a stale receipt's exact stored fields.
type RepairMember struct{ Name, Digest, Action, RunID, ReceiptDigest string }

type repairReceipt struct {
	Key, Digest, RunID, Completed string
	Present                       bool
}

func repairOwnedRun(b *model.Bench, id string) bool {
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

func repairSuccess(tx *sql.Tx, b *model.Bench, id string) bool {
	if id == "" || !repairOwnedRun(b, id) {
		return false
	}
	r, err := history.Get(tx, id)
	return err == nil && r.Key == b.Key && r.Holder == b.Lease.Holder && r.Origin == "supervised" && r.Interruption == nil && r.File != nil && !r.SealedAt.IsZero() && r.ExitCode != nil && *r.ExitCode == 0 && r.ReleasedAt.IsZero()
}

func repairReceiptState(tx *sql.Tx, b *model.Bench, member, digest string) (string, bool, error) {
	r := repairReceipt{Key: receipt(b, member)}
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
	valid := r.Present && r.Digest == digest && parseErr == nil && !completed.IsZero() && !completed.After(time.Now()) && repairSuccess(tx, b, r.RunID)
	return hex.EncodeToString(sum[:]), valid, nil
}

// InspectInitializationRepair performs no mutation. Its caller holds the keyed
// launch gate, state transaction and independently fenced revoked init owner.
func (m Manager) InspectInitializationRepair(ctx context.Context, b *model.Bench, tx *sql.Tx, rerunOnce []string) ([]RepairMember, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tx == nil || b == nil || b.Workspace == nil || b.Lease == nil || b.Lease.Holder != "initialization" || !b.Lease.StopRequested || b.Lease.Continuation != "" || b.Workspace.Archive != nil || (b.Workspace.Phase != "initializing" && b.Workspace.Phase != "prepared") || b.Path != filepath.Join(m.Data, "benches", b.Key) {
		return nil, errors.New("initialization repair requires a revoked held initialization owner")
	}
	if err := m.stopped(b); err != nil {
		return nil, err
	}
	vector, err := (workspace.Lifecycle{State: m.State, Data: m.Data, Git: m.Git}).Inspect(ctx, b, b.Workspace.Pending != nil)
	if err != nil {
		return nil, err
	}
	if b.Workspace.Pending != nil && !reflect.DeepEqual(vector, b.Workspace.Pending.Members) {
		return nil, errors.New("pending initialization vector changed; preparation repair is forbidden")
	}
	choices := map[string]bool{}
	for _, name := range rerunOnce {
		if choices[name] {
			return nil, errors.New("duplicate initialization once choice")
		}
		choices[name] = true
	}
	var out []RepairMember
	for _, member := range b.Workspace.Members {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, digest, err := inputs(member.Path)
		if err != nil {
			return nil, err
		}
		if err := setup.MatchQuota(c, member.RequestedQuotaBytes); err != nil {
			return nil, err
		}
		r := RepairMember{Name: member.Name, Digest: digest, Action: "prepare"}
		var validReceipt bool
		r.ReceiptDigest, validReceipt, err = repairReceiptState(tx, b, member.Name, digest)
		if err != nil {
			return nil, err
		}
		p := proof(b, member.Name)
		if p != nil && p.Digest == digest {
			one := workspace.Clone(b)
			one.Workspace.Members, one.Workspace.Preparation = []model.WorkspaceMember{member}, []model.WorkspacePreparation{*p}
			if CheckPrepared(one) == nil && verifyProof(tx, b, p) == nil && (p.RunID == "" || repairSuccess(tx, b, p.RunID)) {
				r.Action, r.RunID = "preserve", p.RunID
			}
		}
		if stage := b.Workspace.Stage; stage != nil && stage.Name == member.Name && b.Lease.Execution != nil {
			id := b.Lease.Execution.ID
			h, err := history.Get(tx, id)
			if err != nil {
				return nil, err
			}
			if h.Key != b.Key || h.Holder != b.Lease.Holder || h.Origin != "supervised" || !h.ReleasedAt.IsZero() || h.SealedAt.IsZero() || h.Interruption == nil && (h.File == nil || h.ExitCode == nil) {
				return nil, errors.New("initialization stage history requires reconciliation first")
			}
			if stage.Digest == digest && repairSuccess(tx, b, id) {
				r.Action, r.RunID = "salvage", id
			} else if !repairSuccess(tx, b, id) && r.Action == "prepare" && c != nil && strings.TrimSpace(c.Setup.Once) != "" && !validReceipt {
				if !choices[member.Name] {
					return nil, errors.New("uncertain once hook requires explicit member rerun approval")
				}
				r.Action = "rerun-once"
			}
		}
		if marker := b.Workspace.InitializationRepair; marker != nil && r.Action == "prepare" && c != nil && strings.TrimSpace(c.Setup.Once) != "" && !validReceipt {
			for _, uncertain := range marker.UncertainOnce {
				if uncertain == member.Name {
					if !choices[member.Name] {
						return nil, errors.New("historical uncertain once hook requires explicit member rerun approval")
					}
					r.Action = "rerun-once"
				}
			}
		}
		if choices[member.Name] {
			if r.Action != "rerun-once" {
				return nil, errors.New("once rerun choice does not match an uncertain once hook")
			}
			delete(choices, member.Name)
		}
		if b.Workspace.Pending != nil && r.Action != "preserve" {
			return nil, errors.New("pending initialization checkpoint permits no preparation repair")
		}
		out = append(out, r)
	}
	if len(choices) != 0 {
		return nil, errors.New("unknown initialization once member")
	}
	return out, nil
}

// ApplyInitializationRepair repeats inspection before any mutation. The caller
// must redistribute preserve/salvage RunIDs as new held dependencies before
// continuation; this helper never appends a 65th retained run or releases logs.
func (m Manager) ApplyInitializationRepair(ctx context.Context, b *model.Bench, tx *sql.Tx, approved []RepairMember) error {
	var choices []string
	for _, member := range approved {
		if member.Action == "rerun-once" {
			choices = append(choices, member.Name)
		}
	}
	current, err := m.InspectInitializationRepair(ctx, b, tx, choices)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, approved) {
		return errors.New("initialization preparation changed after repair approval")
	}
	if b.Workspace.Pending != nil {
		return nil
	}
	for i, member := range approved {
		_, validReceipt, err := repairReceiptState(tx, b, member.Name, member.Digest)
		if err != nil {
			return err
		}
		if !validReceipt || member.Action == "rerun-once" {
			if err := setup.Invalidate(tx, receipt(b, member.Name)); err != nil {
				return err
			}
		}
		if member.Action != "preserve" {
			removeProof(b, member.Name)
		}
		if member.Action == "salvage" {
			b.Workspace.Preparation = append(b.Workspace.Preparation, model.WorkspacePreparation{Name: member.Name, Digest: member.Digest, RunID: member.RunID})
		}
		if member.RunID != "" && !validReceipt {
			c, digest, err := inputs(b.Workspace.Members[i].Path)
			if err != nil || digest != member.Digest {
				return errors.New("initialization repair inputs changed before receipt restoration")
			}
			if c != nil && c.Hooks() {
				if err := setup.Record(tx, receipt(b, member.Name), member.Digest, member.RunID); err != nil {
					return err
				}
			}
		}
	}
	sort.Slice(b.Workspace.Preparation, func(i, j int) bool { return b.Workspace.Preparation[i].Name < b.Workspace.Preparation[j].Name })
	return ctx.Err()
}

// Package transferguard verifies durable incoming transfer authority without
// depending on the orchestration that originally approved that transfer.
package transferguard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffguard"
	"path/filepath"
	"reflect"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

var ErrGuard = errors.New("incoming transfer source evidence no longer permits initialization")

type Config struct {
	State, Data string
	Git         func(context.Context, string, ...string) (string, error)
	Timeout     time.Duration
}

// Authority is deliberately separate from a held lease capability. A supplied
// initialization token alone cannot resume either an incoming handoff or repair.
func Authority(b *model.Bench, transferID, repairID string) error {
	if b == nil || b.Workspace == nil || b.Workspace.Phase == "ready" {
		return nil
	}
	if r := b.Workspace.InitializationRepair; r != nil {
		if repairID == "" || repairID != r.ID {
			return errors.New("initialization requires its explicit repair continuation")
		}
	} else if o := b.Workspace.TransferredFrom; o != nil && (transferID == "" || transferID != o.ID) {
		return errors.New("incoming transfer requires its explicit continuation")
	}
	return nil
}

type member struct {
	Name, IntegrationBase string
	Present               *worktreetransfer.Plan
	Missing               *worktreetransfer.MissingPlan
}
type repositorySpec struct{ Name, Repository, Branch, Base string }
type approval struct {
	Version, Root, DataRoot                                  string
	ID, SourceKey, SourceID, TargetKey, TargetID, CreationID string
	SourceRowDigest, CheckpointRepository                    string
	Checkpoint                                               *model.WorkspaceCheckpoint
	Repositories                                             []repositorySpec
	QuotaBytes                                               uint64
	Storage                                                  hoststorage.StorageIdentity
	Recovery                                                 model.WorkspaceTransferRecovery
	Members                                                  []member
	Digest                                                   string
}

// Check must run under the enclosing state/host exclusion, using its current
// transaction. Ready operational targets have no perpetual source dependency.
func Check(ctx context.Context, c Config, db reftransfer.Reader, target *model.Bench) error {
	if target == nil || target.Workspace == nil || target.Workspace.Phase == "ready" || target.Workspace.TransferredFrom == nil {
		return nil
	}
	if target.Workspace.TransferredFrom.CrossHost != nil {
		return handoffguard.Current(ctx, db, target)
	}
	if err := check(ctx, c, db, target); err != nil {
		return errors.Join(ErrGuard, err)
	}
	return nil
}
func check(ctx context.Context, c Config, db reftransfer.Reader, target *model.Bench) error {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = repository.DefaultTimeout
	}
	if timeout < 0 || timeout > time.Hour || c.Git == nil || db == nil || !filepath.IsAbs(c.Data) || filepath.Clean(c.Data) != c.Data || !filepath.IsAbs(c.State) || filepath.Clean(c.State) != c.State {
		return errors.New("invalid incoming transfer verifier")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	o := target.Workspace.TransferredFrom
	j, err := reftransfer.Get(db, o.ID)
	if err != nil {
		return err
	}
	var p approval
	if err = json.Unmarshal(j.Plan, &p); err != nil {
		return err
	}
	// Generated approvals always put Digest last. Replacing only that final
	// scalar preserves every approved byte and rejects unknown trailing fields.
	raw := bytes.TrimSpace(j.Plan)
	suffix := []byte(`"Digest":"` + j.Digest + `"}`)
	if !bytes.HasSuffix(raw, suffix) {
		return errors.New("transfer approval is not canonical")
	}
	unsigned := append(bytes.Clone(raw[:len(raw)-len(suffix)]), []byte(`"Digest":""}`)...)
	sum := sha256.Sum256(unsigned)
	if hex.EncodeToString(sum[:]) != j.Digest || p.Digest != j.Digest || o.Digest != j.Digest || p.ID != j.ID || p.SourceKey != j.SourceKey || p.SourceID != j.SourceID || p.TargetKey != j.TargetKey || p.TargetID != j.TargetID || p.CreationID != j.CreationID || o.SourceKey != j.SourceKey || o.SourceID != j.SourceID || target.Key != j.TargetKey || target.Workspace.ID != j.TargetID || target.Workspace.CreationID != j.CreationID || target.Path != filepath.Join(c.Data, "benches", j.TargetKey) || p.Root != c.State || p.DataRoot != c.Data || j.Phase != "relinquished" {
		return errors.New("incoming transfer identity differs from its approved journal")
	}
	var source model.Bench
	var record []byte
	if err = db.QueryRow("SELECT record FROM benches WHERE key=?", j.SourceKey).Scan(&record); err != nil {
		return err
	}
	if err = json.Unmarshal(record, &source); err != nil {
		return err
	}
	if source.Key != j.SourceKey || source.Workspace == nil || source.Workspace.ID != j.SourceID || source.Path != filepath.Join(c.Data, "benches", j.SourceKey) || source.Lease != nil || source.Workspace.Transfer == nil || source.Workspace.Pending != nil || !reflect.DeepEqual(source.Workspace.Checkpoint, p.Checkpoint) {
		return errors.New("incoming transfer source is not its permanently retired checkpoint")
	}

	if p.Checkpoint == nil || target.Workspace.CheckpointRepository != source.Workspace.CheckpointRepository || p.CheckpointRepository != source.Workspace.CheckpointRepository {
		return errors.New("incoming transfer checkpoint or quota differs")
	}
	if err := checkQuota(ctx, c, p, target); err != nil {
		return err
	}
	var repository string
	for _, m := range source.Workspace.Members {
		if m.Name == p.CheckpointRepository {
			repository = m.Repository
		}
	}
	expectedOrigin := &model.WorkspaceRecovery{Repository: repository, BenchID: j.SourceID, Commit: p.Checkpoint.Seal, ManifestSHA256: p.Checkpoint.Digest}
	if repository == "" || !reflect.DeepEqual(target.Workspace.RecoveredFrom, expectedOrigin) {
		return errors.New("incoming transfer sealed provenance differs")
	}
	marker := source.Workspace.Transfer
	if marker.ID != j.ID || marker.Digest != j.Digest || marker.TargetKey != j.TargetKey || marker.TargetID != j.TargetID || marker.CreationID != j.CreationID || marker.Phase != "transferred" {
		return errors.New("incoming transfer source tombstone differs")
	}
	switch p.Version {
	case "superbench.workspace-transfer.v1":
		if marker.Recovery != nil {
			return errors.New("ordinary transfer cannot carry missing-source evidence")
		}
		return nil
	case "superbench.workspace-missing-transfer.v1":
		if marker.Recovery == nil || !reflect.DeepEqual(*marker.Recovery, p.Recovery) || p.Recovery.Mode != "missing" || p.Recovery.SourceRowDigest != p.SourceRowDigest {
			return errors.New("incoming missing-source retirement differs")
		}
	default:
		return errors.New("unknown incoming transfer approval version")
	}
	return retained(ctx, c, p, &source, target)
}

func retained(ctx context.Context, c Config, p approval, source, target *model.Bench) error {
	if len(p.Members) < 1 || len(p.Members) > 32 || len(p.Members) != len(source.Workspace.Members) || len(p.Members) != len(target.Workspace.Members) || len(p.Repositories) != len(p.Members) || p.Checkpoint == nil || len(p.Checkpoint.Members) != len(p.Members) || p.CheckpointRepository != source.Workspace.CheckpointRepository || p.CheckpointRepository != target.Workspace.CheckpointRepository || p.Storage.Root != c.Data || p.Storage.BenchKey != source.Key {
		return errors.New("incoming missing-source vector differs")
	}
	if p.Storage.Assignment.Path != source.Path || p.Storage.Assignment.ProjectID != source.ProjectID || p.Storage.Assignment.Bytes != source.QuotaBytes || p.Storage.Assignment.Inodes != source.QuotaInodes {
		return errors.New("source storage assignment changed")
	}
	worker := hoststorage.Worker{State: c.State, Data: c.Data}
	if err := worker.VerifyStorage(ctx, p.Storage); err != nil {
		return err
	}
	storage, err := p.Storage.Bytes()
	if err != nil {
		return err
	}
	missing := worktreetransfer.MissingManager{Git: c.Git, Timeout: c.Timeout, VerifyStorage: func(ctx context.Context, raw json.RawMessage) error {
		id, err := hoststorage.ParseStorageIdentity(raw)
		if err != nil {
			return err
		}
		if id != p.Storage {
			return errors.New("member storage proof differs")
		}
		return worker.VerifyStorage(ctx, id)
	}}
	present := worktreetransfer.Manager{Git: c.Git, Timeout: c.Timeout}
	absent := false
	for i, m := range p.Members {
		s, t, r, cp := source.Workspace.Members[i], target.Workspace.Members[i], p.Repositories[i], p.Checkpoint.Members[i]
		if m.Name != s.Name || m.Name != t.Name || m.Name != r.Name || m.Name != cp.Name || i > 0 && p.Members[i-1].Name >= m.Name || r.Base != "" || s.Repository != r.Repository || t.Repository != r.Repository || s.Branch != r.Branch || t.Branch != r.Branch || s.Mirror != t.Mirror || s.Base != m.IntegrationBase || t.Base != m.IntegrationBase || t.Source != cp.Commit || s.Path != filepath.Join(source.Path, m.Name) || t.Path != filepath.Join(target.Path, m.Name) || (m.Present == nil) == (m.Missing == nil) {
			return errors.New("incoming missing-source member differs")
		}
		if m.Missing != nil {
			absent = true
			spec := m.Missing.Spec
			if spec.ID != p.ID || spec.Checkout != s.Path || spec.Common != s.Mirror || spec.TaskRef != "refs/heads/"+s.Branch || spec.Commit != cp.Commit || !bytes.Equal(spec.Storage, storage) {
				return errors.New("missing member authority differs")
			}
			if err := missing.VerifyRetained(ctx, *m.Missing); err != nil {
				return fmt.Errorf("source member %s: %w", m.Name, err)
			}
		} else {
			old := m.Present
			if old.Checkout != s.Path || old.Common != s.Mirror || old.TaskRef != "refs/heads/"+s.Branch || old.Commit != cp.Commit {
				return errors.New("present member authority differs")
			}
			current, err := present.InspectDetached(ctx, old.Checkout, old.Common, old.TaskRef, old.Commit)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(current, *old) {
				return errors.New("retained present source changed")
			}
		}
	}
	if !absent {
		return errors.New("missing-source approval contains no missing member")
	}
	return ctx.Err()
}

package workspacetransfer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

func (m Manager) storageWorker() hoststorage.Worker {
	return hoststorage.Worker{State: m.Store.State, Data: m.Store.Data}
}
func (m Manager) missingGit(a workspacerepair.Manager) worktreetransfer.MissingManager {
	return worktreetransfer.MissingManager{Git: a.Prep.Git, Timeout: m.Store.MirrorTimeout, VerifyStorage: func(ctx context.Context, raw json.RawMessage) error {
		proof, err := hoststorage.ParseStorageIdentity(raw)
		if err != nil {
			return err
		}
		return m.storageWorker().VerifyStorage(ctx, proof)
	}}
}

func (m Manager) InspectMissing(ctx context.Context, source, target, id string, quota uint64) (*MissingPlan, error) {
	if !keyPattern.MatchString(source) || !keyPattern.MatchString(target) || !keyPattern.MatchString(id) || source == target || !quotapolicy.ValidBytes(quota) {
		return nil, errors.New("missing-source transfer requires distinct source/destination keys and a fresh ID")
	}
	ctx, cancel, a, err := m.admin(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	targetID, err := model.Nonce()
	if err != nil {
		return nil, err
	}
	p := MissingPlan{Version: MissingVersion, Root: m.Store.State, DataRoot: m.Store.Data, ID: id, SourceKey: source, TargetKey: target, TargetID: targetID, CreationID: id, QuotaBytes: quota, ObservedAt: time.Now().UTC()}
	var locks transferCaptures
	err = a.Prep.Gate(source, func() error {
		defer func() { locks.close() }()
		return a.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			var e error
			p, e = m.inspectMissingState(ctx, a, rows, tx, p, &locks)
			return e
		})
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (m Manager) inspectMissingState(ctx context.Context, a workspacerepair.Manager, rows benchstate.Database, tx *sql.Tx, p MissingPlan, locks *transferCaptures) (MissingPlan, error) {
	b := rows[p.SourceKey]
	if err := workspacepolicy.Validate(b); err != nil {
		return p, err
	}
	if b.Workspace.Transfer != nil || b.Workspace.Phase != "ready" || b.Workspace.Archive != nil || b.Workspace.Pending != nil || b.Workspace.Checkpoint == nil || rows[p.TargetKey] != nil || b.Path != filepath.Join(m.Store.Data, "benches", b.Key) {
		return p, errors.New("missing-source transfer requires a ready source, complete checkpoint, no pending publication and unused destination")
	}
	if b.Lease != nil && (!b.Lease.StopRequested || b.Lease.Continuation != "" || b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped") {
		return p, errors.New("explicitly stop and fence the source before missing-checkout review")
	}
	for _, path := range []string{filepath.Join(m.Store.Data, "benches", p.TargetKey), filepath.Join(m.Store.State, "create-"+p.TargetKey+".json")} {
		if err := absent(path); err != nil {
			return p, err
		}
	}
	if b.Lease != nil && b.Lease.Execution != nil {
		if err := a.Prep.Backend.Fence(b.Lease.Execution); err != nil {
			return p, err
		}
	}
	if err := a.Lifecycle.Stopped(workspace.Clone(b)); err != nil {
		return p, err
	}
	storage, err := m.storageWorker().InspectStorage(ctx, b.Key, hoststorage.Assignment{Path: b.Path, ProjectID: b.ProjectID, Bytes: b.QuotaBytes, Inodes: b.QuotaInodes})
	if err != nil {
		return p, err
	}
	p.Storage = storage
	if err = missingRootInventory(b); err != nil {
		return p, err
	}
	// This verifies immutable seals/pins and current remote task refs without
	// claiming that missing files or unknown work were checkpointed.
	if err = a.Lifecycle.Confirm(ctx, b); err != nil {
		return p, err
	}
	p.SourceID, p.CheckpointRepository = b.Workspace.ID, b.Workspace.CheckpointRepository
	p.SourceRowDigest, err = hash(b)
	if err != nil {
		return p, err
	}
	p.Checkpoint = workspace.Clone(b).Workspace.Checkpoint
	p.History, err = snapshotTransferHistory(ctx, m.Store.State, b, tx, locks)
	if err != nil {
		return p, err
	}
	p.Recovery = model.WorkspaceTransferRecovery{Mode: "missing", SourceRowDigest: p.SourceRowDigest, RetiredAt: p.ObservedAt.Format(time.RFC3339Nano), Runs: make([]model.WorkspaceRetiredRun, 0, len(p.History))}
	for _, h := range p.History {
		p.Recovery.Runs = append(p.Recovery.Runs, model.WorkspaceRetiredRun{ID: h.ID, Digest: h.Digest})
	}
	if b.Lease != nil {
		copy := workspace.Clone(b)
		p.Recovery.Holder, p.Recovery.Execution, p.Recovery.Stage = copy.Lease.Holder, copy.Lease.Execution, copy.Workspace.Stage
	}
	p.Members, p.Repositories = nil, nil
	storageRaw, err := storage.Bytes()
	if err != nil {
		return p, err
	}
	w := worktreetransfer.Manager{Git: a.Prep.Git, Timeout: m.Store.MirrorTimeout}
	missing := m.missingGit(a)
	for i, member := range b.Workspace.Members {
		entry := MissingMemberPlan{Name: member.Name, IntegrationBase: member.Base}
		if _, e := os.Lstat(member.Path); e == nil {
			plan, e := w.Inspect(ctx, member.Path, member.Mirror, "refs/heads/"+member.Branch, p.Checkpoint.Members[i].Commit)
			if e != nil {
				return p, e
			}
			entry.Present = &plan
		} else if os.IsNotExist(e) {
			plan, e := missing.Inspect(ctx, worktreetransfer.MissingSpec{ID: p.ID, Checkout: member.Path, Common: member.Mirror, TaskRef: "refs/heads/" + member.Branch, Commit: p.Checkpoint.Members[i].Commit, Storage: storageRaw})
			if e != nil {
				return p, e
			}
			entry.Missing = &plan
		} else {
			return p, e
		}
		p.Members = append(p.Members, entry)
		p.Repositories = append(p.Repositories, workspace.Repository{Name: member.Name, Repository: member.Repository, Branch: member.Branch})
	}
	if err = locks.check(m.Store.State); err != nil {
		return p, err
	}
	p.Digest, err = p.digest()
	if err != nil {
		return p, err
	}
	return p, p.validate()
}

func missingRootInventory(b *model.Bench) error {
	entries, err := os.ReadDir(b.Path)
	if os.IsNotExist(err) {
		return nil // Each missing member still needs its anchored storage proof.
	}
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(b.Path)
	if err != nil || root != b.Path {
		return errors.New("missing-source root was replaced or contains a symlink")
	}
	known := map[string]bool{}
	for _, member := range b.Workspace.Members {
		known[member.Name] = true
	}
	for _, entry := range entries {
		if !known[entry.Name()] || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("missing-source root contains an unreviewed entry")
		}
	}
	return nil
}

package workspacetransfer

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

func (m Manager) Inspect(ctx context.Context, source, target, id string, quota uint64) (*Plan, error) {
	if !keyPattern.MatchString(source) || !keyPattern.MatchString(target) || !keyPattern.MatchString(id) || source == target || !quotapolicy.ValidBytes(quota) {
		return nil, errors.New("transfer requires distinct source/destination keys and a fresh ID")
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
	p := Plan{Version: Version, Root: m.Store.State, DataRoot: m.Store.Data, ID: id, SourceKey: source, TargetKey: target, TargetID: targetID, CreationID: id, QuotaBytes: quota, ObservedAt: time.Now().UTC()}
	err = a.Prep.Gate(source, func() error {
		return a.Prep.Update(func(rows benchstate.Database, _ *sql.Tx) error {
			var e error
			p, e = m.inspectState(ctx, a, rows, p)
			return e
		})
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (m Manager) inspectState(ctx context.Context, a workspacerepair.Manager, rows benchstate.Database, p Plan) (Plan, error) {
	b := rows[p.SourceKey]
	if err := workspacepolicy.Validate(b); err != nil {
		return p, err
	}
	if fresh, err := freshtree.Selected(nil, b); err != nil || fresh {
		return p, errors.Join(freshtree.ErrUnavailable, err) // a Fresh tree is never transferred
	}
	if b.Workspace.Transfer != nil || b.Workspace.Phase != "ready" || b.Workspace.Archive != nil || b.Lease != nil || b.Workspace.Stage != nil || b.Workspace.Pending != nil || b.Workspace.Checkpoint == nil || rows[p.TargetKey] != nil {
		return p, errors.New("transfer requires an unleased ready source with a complete checkpoint and an unused destination")
	}
	for _, path := range []string{filepath.Join(m.Store.Data, "benches", p.TargetKey), filepath.Join(m.Store.State, "create-"+p.TargetKey+".json")} {
		if err := absent(path); err != nil {
			return p, err
		}
	}
	if err := a.Lifecycle.Stopped(workspace.Clone(b)); err != nil {
		return p, err
	}
	vector, err := a.Lifecycle.Inspect(ctx, b, true)
	if err != nil {
		return p, err
	}
	if !reflect.DeepEqual(vector, b.Workspace.Checkpoint.Members) {
		return p, errors.New("source must checkpoint its exact current vector before transfer")
	}
	if err = a.Lifecycle.Confirm(ctx, b); err != nil {
		return p, err
	}
	p.SourceID = b.Workspace.ID
	p.SourceRowDigest, err = hash(b)
	if err != nil {
		return p, err
	}
	p.Checkpoint = workspace.Clone(b).Workspace.Checkpoint
	p.CheckpointRepository = b.Workspace.CheckpointRepository
	p.Members, p.Repositories = nil, nil
	w := worktreetransfer.Manager{Git: a.Prep.Git, Timeout: m.Store.MirrorTimeout}
	for i, member := range b.Workspace.Members {
		plan, e := w.Inspect(ctx, member.Path, member.Mirror, "refs/heads/"+member.Branch, vector[i].Commit)
		if e != nil {
			return p, e
		}
		p.Members = append(p.Members, MemberPlan{Name: member.Name, IntegrationBase: member.Base, Worktree: plan})
		p.Repositories = append(p.Repositories, workspace.Repository{Name: member.Name, Repository: member.Repository, Branch: member.Branch})
	}
	p.Digest, err = p.digest()
	if err != nil {
		return p, err
	}
	return p, p.validate()
}

package workspacetransfer

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
)

type Manager struct{ Store bench.Store }
type Result struct {
	Version       string `json:"version"`
	ID            string `json:"transfer_id"`
	SourceKey     string `json:"source_key"`
	SourceID      string `json:"source_id"`
	TargetKey     string `json:"target_key"`
	TargetID      string `json:"target_id"`
	Phase         string `json:"phase"`
	GrantToken    string `json:"grant_token,omitempty"`
	RootPath      string `json:"root_path,omitempty"`
	StopRequested bool   `json:"stop_requested"`
}

func (m Manager) admin(ctx context.Context) (context.Context, context.CancelFunc, workspacerepair.Manager, error) {
	a, err := m.Store.WorkspaceRepair()
	if err != nil {
		return ctx, func() {}, a, err
	}
	timeout := m.Store.MirrorTimeout
	if timeout == 0 {
		timeout = repository.DefaultTimeout
	}
	if timeout < 0 || timeout > time.Hour {
		return ctx, func() {}, a, errors.New("invalid workspace transfer timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	if _, native := a.Prep.Backend.(execution.Host); native && execution.Supported {
		root := a.Prep.CgroupRoot
		if root == "" {
			root = "/sys/fs/cgroup/superbench"
		}
		if gitcontrol.Available(root) {
			state, e := filepath.EvalSymlinks(a.Prep.State)
			if e != nil {
				cancel()
				return ctx, func() {}, a, e
			}
			ctx = gitcontrol.WithRunner(ctx, &gitcontrol.Runner{State: state, Root: root, Timeout: timeout})
		}
	}
	return ctx, cancel, a, ctx.Err()
}

func boundRecord(rows benchstate.Database, tx *sql.Tx, id string) (reftransfer.Record, *model.Bench, error) {
	j, err := reftransfer.Get(tx, id)
	if err != nil {
		return j, nil, err
	}
	b := rows[j.SourceKey]
	if b == nil || b.Workspace == nil || b.Workspace.ID != j.SourceID || b.Lease != nil || b.Workspace.Transfer == nil {
		return j, nil, errors.New("workspace transfer source ownership changed")
	}
	t := b.Workspace.Transfer
	if t.CrossHost != nil {
		return j, nil, errors.New("cross-host export cannot resume a local transfer")
	}
	if t.ID != j.ID || t.Digest != j.Digest || t.TargetKey != j.TargetKey || t.TargetID != j.TargetID || t.CreationID != j.CreationID || (j.Phase == "started") != (t.Phase == "relinquishing") {
		return j, nil, errors.New("workspace transfer marker differs from its durable journal")
	}
	return j, b, nil
}

func projection(j reftransfer.Record, b *model.Bench, grant bool) *Result {
	r := &Result{Version: Version, ID: j.ID, SourceKey: j.SourceKey, SourceID: j.SourceID, TargetKey: j.TargetKey, TargetID: j.TargetID, Phase: j.Phase}
	if b == nil || b.Workspace == nil || b.Workspace.ID != j.TargetID || b.Workspace.TransferredFrom == nil || b.Workspace.TransferredFrom.ID != j.ID || b.Workspace.TransferredFrom.Digest != j.Digest || b.Workspace.TransferredFrom.SourceID != j.SourceID || b.Workspace.TransferredFrom.SourceKey != j.SourceKey {
		return r
	}
	if b.Lease == nil && b.Workspace.Phase == "ready" {
		r.RootPath = b.Path
	} else if b.Lease != nil && b.Lease.Holder == "initialization" && b.Workspace.InitializationRepair == nil && j.Phase != "completed" && (b.Workspace.Phase == "materializing" || b.Workspace.Phase == "initializing" || b.Workspace.Phase == "prepared") {
		r.StopRequested = b.Lease.StopRequested
		r.RootPath = b.Path
		if grant && !b.Lease.StopRequested && b.Lease.Continuation != "" {
			r.GrantToken = b.Lease.Token
		}
	}
	return r
}

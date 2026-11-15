package workspacerepair

import (
	"context"
	"crypto/subtle"
	"errors"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statepath"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

type Manager struct {
	Prep      workspaceprep.Manager
	Lifecycle workspace.Lifecycle
}

// Result is the entire local output DTO. Supplied capabilities are not echoed.
type Result struct {
	Version       string `json:"version"`
	Key           string `json:"key"`
	BenchID       string `json:"bench_id"`
	Phase         string `json:"phase"`
	RepairID      string `json:"repair_id"`
	StopRequested bool   `json:"stop_requested"`
	GrantToken    string `json:"grant_token,omitempty"`
	RootPath      string `json:"root_path,omitempty"`
}

func (m Manager) controlled(ctx context.Context) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := m.Prep
	if p.Read == nil || p.Update == nil || p.Gate == nil || p.Admit == nil || p.Backend == nil || p.Git == nil || m.Lifecycle.Within == nil || m.Lifecycle.Git == nil || m.Lifecycle.Released == nil {
		return nil, errors.New("initialization repair requires lifecycle and state authority")
	}
	if err := statepath.Validate(p.State, p.Data); err != nil {
		return nil, err
	}
	if filepath.Clean(p.State) != p.State || filepath.Clean(p.Data) != p.Data {
		return nil, errors.New("initialization repair requires normalized storage")
	}
	if _, native := p.Backend.(execution.Host); native && execution.Supported {
		root := p.CgroupRoot
		if root == "" {
			root = "/sys/fs/cgroup/superbench"
		}
		if gitcontrol.Available(root) {
			state, err := filepath.EvalSymlinks(p.State)
			if err != nil {
				return nil, err
			}
			ctx = gitcontrol.WithRunner(ctx, &gitcontrol.Runner{State: state, Root: root, Timeout: m.Lifecycle.Timeout})
		}
	}
	return ctx, nil
}

func stoppedOwner(rows benchstate.Database, key string) (*model.Bench, error) {
	b := rows[key]
	if b == nil || b.Workspace == nil || b.Workspace.Archive != nil || b.Lease == nil || b.Lease.Holder != "initialization" || !b.Lease.StopRequested || b.Lease.Continuation != "" || b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped" || b.Workspace.Phase != "initializing" && b.Workspace.Phase != "prepared" {
		return nil, errors.New("initialization repair requires an explicitly stopped initialization owner")
	}
	return b, nil
}

func (m Manager) fence(b *model.Bench) error {
	if b.Lease.Execution == nil {
		return nil
	}
	if err := m.Prep.Backend.Fence(b.Lease.Execution); err != nil {
		return err
	}
	return m.Prep.Backend.Empty(b.Lease.Execution)
}

func projection(b *model.Bench, id string, root bool) *Result {
	r := &Result{Version: Version, Key: b.Key, BenchID: b.Workspace.ID, Phase: b.Workspace.Phase, RepairID: id}
	if b.Lease != nil {
		r.StopRequested = b.Lease.StopRequested
	}
	if root {
		r.RootPath = b.Path
	}
	return r
}

func (m Manager) view(key, id, token string, grant bool) (*Result, error) {
	var out *Result
	err := m.Prep.Read(func(rows benchstate.Database) error {
		b := rows[key]
		if b == nil || b.Workspace == nil || b.Workspace.InitializationRepair == nil || b.Workspace.InitializationRepair.ID != id {
			return errors.New("initialization repair ownership changed")
		}
		if b.Lease == nil {
			if b.Workspace.Phase != "ready" {
				return errors.New("initialization repair has no current owner")
			}
			out = projection(b, id, true)
			return nil
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(b.Lease.Token)) != 1 || b.Lease.Holder != "initialization" || b.Workspace.Phase == "ready" {
			return errors.New("initialization repair owner was replaced")
		}
		out = projection(b, id, true)
		if grant && !b.Lease.StopRequested && b.Lease.Continuation != "" {
			out.GrantToken = token
		}
		return nil
	})
	return out, err
}

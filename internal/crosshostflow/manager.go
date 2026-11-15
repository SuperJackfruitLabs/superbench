package crosshostflow

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
)

type Manager struct{ Store bench.Store }
type Result struct {
	Version, Key, WorkspaceID, CreationID, TransferID, Phase string
	GrantToken, RootPath                                     string `json:",omitempty"`
	StopRequested                                            bool
}

func result(b *model.Bench, id, phase string, grant bool) *Result {
	r := &Result{Version: Version, TransferID: id, Phase: phase}
	if b == nil || b.Workspace == nil {
		return r
	}
	w := b.Workspace
	r.Key = b.Key
	r.WorkspaceID = w.ID
	r.CreationID = w.CreationID
	if w.Phase == "ready" && b.Lease == nil {
		r.RootPath = b.Path
	}
	if phase != "completed" && w.InitializationRepair == nil && b.Lease != nil && b.Lease.Holder == "initialization" && (w.Phase == "materializing" || w.Phase == "initializing" || w.Phase == "prepared") {
		r.RootPath = b.Path
		r.StopRequested = b.Lease.StopRequested
		if grant && !b.Lease.StopRequested && b.Lease.Continuation != "" {
			r.GrantToken = b.Lease.Token
		}
	}
	return r
}
func (m Manager) admin(ctx context.Context) (context.Context, context.CancelFunc, workspacerepair.Manager, error) {
	if err := ctx.Err(); err != nil {
		return ctx, func() {}, workspacerepair.Manager{}, err
	}
	a, err := m.Store.WorkspaceRepair()
	if err != nil {
		return ctx, func() {}, a, err
	}
	timeout := m.Store.MirrorTimeout
	if timeout == 0 {
		timeout = repository.DefaultTimeout
	}
	if timeout < 0 || timeout > time.Hour {
		return ctx, func() {}, a, errors.New("invalid handoff timeout")
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

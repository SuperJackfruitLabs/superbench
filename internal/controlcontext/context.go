// Package controlcontext configures existing Git supervision without owning a
// workspace, starting commands or deciding whether a writer has been fenced.
package controlcontext

import (
	"context"
	"path/filepath"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

type Config struct {
	Existing           context.Context
	State, Root        string
	Timeout            time.Duration
	Required, Injected bool
	Migrate            func() error
}

// Configure preserves native/unmanaged fallback and commits the schema barrier
// before a managed control child could outlive its controller. Configuration
// alone never certifies a fence.
func Configure(ctx context.Context, c Config) (context.Context, error) {
	if c.Existing != nil {
		return c.Existing, nil
	}
	if !execution.Supported || c.Injected {
		return ctx, nil
	}
	root := c.Root
	if root == "" {
		root = "/sys/fs/cgroup/superbench"
	}
	if !c.Required && !gitcontrol.Available(root) {
		return ctx, nil
	}
	if c.Migrate == nil {
		return nil, operationcontrol.ErrAuthority
	}
	if err := c.Migrate(); err != nil {
		return nil, err
	}
	state, err := filepath.EvalSymlinks(c.State)
	if err != nil {
		return nil, err
	}
	controls := gitcontrol.WithRunner(ctx, &gitcontrol.Runner{State: state, Root: root, Timeout: c.Timeout})
	if _, ok := operationcontrol.FromContext(ctx); ok {
		if !gitcontrol.Available(root) {
			return controls, operationcontrol.ErrAuthority
		}
		identity, err := execution.CurrentIdentity()
		if err != nil {
			return controls, err
		}
		selected, err := operationcontrol.WithControl(controls, operationcontrol.Control{State: state, Root: root, HostID: identity.HostID, BootID: identity.BootID})
		if err != nil {
			return controls, err
		}
		controls = selected
	}
	return controls, nil
}

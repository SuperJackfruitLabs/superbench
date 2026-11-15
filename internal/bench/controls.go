package bench

import (
	"context"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
)

func (s Store) controlContext() context.Context {
	if s.controls != nil {
		return s.controls
	}
	return context.Background()
}

// Commit the schema barrier before a control child can outlive its controller.
// A failed outer operation must not roll this barrier back to an older binary.
func (s Store) withControls(ctx context.Context, required bool) (Store, context.Context, error) {
	if s.controls != nil {
		return s, s.controls, nil
	}
	if !execution.Supported || s.executor != nil {
		return s, ctx, nil
	}
	root := s.CgroupRoot
	if root == "" {
		root = "/sys/fs/cgroup/superbench"
	}
	if !required && !gitcontrol.Available(root) {
		return s, ctx, nil
	}
	if err := s.Migrate(); err != nil {
		return s, ctx, err
	}
	state, err := filepath.EvalSymlinks(s.State)
	if err != nil {
		return s, ctx, err
	}
	s.controls = gitcontrol.WithRunner(ctx, &gitcontrol.Runner{State: state, Root: root, Timeout: s.MirrorTimeout})
	return s, s.controls, nil
}

func (s Store) reconciliationControls(key string) (Store, error) {
	if err := s.RequireLegacy(key); err != nil {
		return s, err
	}
	if s.controls != nil || !execution.Supported || s.executor != nil {
		return s, nil
	}
	if err := s.Migrate(); err != nil {
		return s, err
	}
	rows, err := s.Status(key)
	if err != nil {
		return s, err
	}
	required := len(rows) == 1 && rows[0].Lease != nil && rows[0].Lease.Execution != nil
	if required {
		s.CgroupRoot = rows[0].Lease.Execution.Root
	}
	s, _, err = s.withControls(context.Background(), required)
	return s, err
}

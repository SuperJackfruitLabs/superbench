package bench

import (
	"context"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/controlcontext"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
)

func (s Store) controlContext() context.Context {
	if s.controls != nil {
		return s.controls
	}
	return context.Background()
}

// Commit the schema barrier before a control child can outlive its controller.
func (s Store) withControls(ctx context.Context, required bool) (Store, context.Context, error) {
	controls, err := controlcontext.Configure(ctx, controlcontext.Config{Existing: s.controls, State: s.State, Root: s.CgroupRoot, Timeout: s.MirrorTimeout, Required: required, Injected: s.executor != nil, Migrate: s.Migrate})
	s.controls = controls
	if err != nil {
		return s, ctx, err
	}
	return s, controls, nil
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

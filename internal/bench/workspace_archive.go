package bench

import (
	"context"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacearchive"
)

type WorkspaceReapPlan = workspacearchive.Plan

func (s Store) workspaceArchiver() workspacearchive.Manager {
	return workspacearchive.Manager{State: s.State, Data: s.Data, Timeout: s.MirrorTimeout,
		Within: s.workspaceWithin, Git: s.workspaceGit, SaveIntent: s.saveLifecycleIntent,
		ReleaseQuota: s.releaseQuota, AfterDelete: s.reapAfterDelete, Busy: ErrBusy}
}

// ArchiveWorkspaceContext retains a whole-vector read-only intent before
// changing checkout permissions. The optional check gates only a new intent.
func (s Store) ArchiveWorkspaceContext(ctx context.Context, key string, grace time.Duration, check func(context.Context, *Bench) error) (*Bench, error) {
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	return s.workspaceArchiver().Archive(ctx, key, grace, check)
}

func (s Store) PlanWorkspaceReapContext(ctx context.Context, key string) (*WorkspaceReapPlan, error) {
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	return s.workspaceArchiver().PlanReap(ctx, key)
}

// ReapWorkspaceContext accepts only the concrete reviewed aggregate manifest.
func (s Store) ReapWorkspaceContext(ctx context.Context, plan WorkspaceReapPlan) (*Bench, error) {
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	return s.workspaceArchiver().Reap(ctx, plan)
}

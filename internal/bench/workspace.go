package bench

import (
	"context"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

// StageWorkspaceContext durably owns and materializes an aggregate workspace.
// It retains initialization ownership; it does not publish a ready workspace.
func (s Store) StageWorkspaceContext(ctx context.Context, creationID string, spec workspace.Spec) (*Bench, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := workspace.ValidateStage(creationID, spec); err != nil {
		return nil, err
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	s, ctx, err := s.withControls(ctx, false)
	if err != nil {
		return nil, err
	}
	m := workspace.Manager{State: s.State, Data: s.Data, Timeout: s.MirrorTimeout, Admit: s.admit, Allocate: s.allocateWorkspace}
	m.Within, m.Git = s.workspaceWithin, s.workspaceGit
	return m.Stage(ctx, creationID, spec)
}

func (s Store) allocateWorkspace(b *Bench) error {
	c, err := s.hostConfig()
	if err != nil {
		return err
	}
	if c == nil {
		if b.ProjectID != 0 {
			return errors.New("managed workspace has no host policy")
		}
		return nil
	}
	a, err := s.workerStorage().Quota(*c, "bench", b.Key, b.RequestedQuotaBytes)
	if err != nil {
		return err
	}
	if a.Path != b.Path || a.ProjectID == 0 || a.Bytes == 0 || a.Inodes == 0 || b.RequestedQuotaBytes != 0 && a.Bytes != b.RequestedQuotaBytes || b.ProjectID != 0 && (b.ProjectID != a.ProjectID || b.QuotaBytes != a.Bytes || b.QuotaInodes != a.Inodes) {
		return errors.New("workspace quota differs from its pinned allocation")
	}
	b.ProjectID, b.QuotaBytes, b.QuotaInodes = a.ProjectID, a.Bytes, a.Inodes
	return nil
}

func (s Store) workspaceWithin(fn func(workspace.Update) error) error {
	return s.session(func(session *statestore.Session) error {
		return fn(func(update workspace.Change) error { return s.update(session, update) })
	})
}
func (s Store) workspaceGit(ctx context.Context, dir string, args ...string) (string, error) {
	s.controls = ctx
	return s.git(dir, args...)
}
func (s Store) workspacePreparation() workspaceprep.Manager {
	return workspaceprep.Manager{State: s.State, Data: s.Data, CgroupRoot: s.CgroupRoot,
		Update: s.transactionSQL, Read: s.read, Gate: s.executionGate, Git: s.workspaceGit,
		Backend: s.backend(), Admit: s.admit, Quota: s.ensureBenchQuota, ReserveLog: s.reserveLog, Seal: s.sealRunLog}
}
func (s Store) workspaceLifecycle() workspace.Lifecycle {
	return workspace.Lifecycle{State: s.State, Data: s.Data, Timeout: s.MirrorTimeout,
		Within: s.workspaceWithin, Git: s.workspaceGit, Stopped: s.ensureStopped,
		Prepared: workspaceprep.CheckPrepared, Released: workspaceprep.ReleaseHistory}
}
func (s Store) workspaceControls(ctx context.Context) (Store, context.Context, error) {
	if err := ctx.Err(); err != nil {
		return s, ctx, err
	}
	if err := s.Migrate(); err != nil {
		return s, ctx, err
	}
	return s.withControls(ctx, false)
}

// PrepareWorkspaceContext retains initialization ownership after preparing the
// selected members. Empty selector prepares every registered member.
func (s Store) PrepareWorkspaceContext(ctx context.Context, key, token, selector string, offline bool) (*Bench, error) {
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	return s.workspacePreparation().Prepare(ctx, key, token, selector, offline)
}

// InitializeWorkspaceContext publishes readiness only after all preparation and
// a complete remotely confirmed genesis checkpoint, releasing the one init lease.
func (s Store) InitializeWorkspaceContext(ctx context.Context, creationID string, spec workspace.Spec) (*Bench, error) {
	b, err := s.StageWorkspaceContext(ctx, creationID, spec)
	if err != nil || b.Workspace.Phase == "ready" {
		return b, err
	}
	s, ctx, err = s.workspaceControls(ctx)
	if err != nil {
		return b, err
	}
	if b.Workspace.Pending == nil {
		b, err = s.workspacePreparation().Prepare(ctx, b.Key, b.Lease.Token, "", false)
		if err != nil {
			return b, err
		}
	}
	return s.workspaceLifecycle().FinishInitialization(ctx, b.Key, b.Lease.Token)
}
func (s Store) CheckpointWorkspaceContext(ctx context.Context, key, token string) (*Bench, error) {
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	return s.workspaceLifecycle().Checkpoint(ctx, key, token)
}
func (s Store) StopWorkspace(key, token string, admin bool) (*Bench, error) {
	if err := s.Migrate(); err != nil {
		return nil, err
	}
	return s.workspacePreparation().Stop(key, token, admin)
}

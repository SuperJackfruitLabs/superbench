package bench

import (
	"context"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacearchive"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacebootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceinit"
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
	if s.ProposedFreshPayload {
		m.Fresh = workspacebootstrap.New(s.State, s.Data)
	}
	return m.Stage(ctx, creationID, spec)
}

func (s Store) allocateWorkspace(b *Bench) error { return s.workerStorage().AssignWorkspace(b) }

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
		Prepared: s.workspacePreparation().Prepared, Released: workspaceprep.ReleaseHistory,
		Admit: s.admit, Allocate: s.allocateWorkspace,
		NoteCheckpoint: func(ctx context.Context, b *Bench) {
			workspacearchive.NoteCheckpoint(ctx, repository.Mirrors{State: s.State, Data: s.Data, Timeout: s.MirrorTimeout}, b)
		}}
}
func (s Store) workspaceControls(ctx context.Context) (Store, context.Context, error) {
	if err := ctx.Err(); err != nil {
		return s, ctx, err
	}
	if _, scoped := operationcontrol.FromContext(ctx); !scoped {
		if err := s.Migrate(); err != nil {
			return s, ctx, err
		}
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
	if err != nil {
		return b, err
	}
	return s.finishWorkspaceInitialization(ctx, b)
}

func (s Store) finishWorkspaceInitialization(ctx context.Context, b *Bench) (*Bench, error) {
	return (workspaceinit.Manager{Stop: func(key, token string) (*Bench, error) {
		return s.workspacePreparation().Stop(key, token, false)
	}, Continue: func(ctx context.Context, b *Bench) (*Bench, error) {
		controlled, child, err := s.workspaceControls(ctx)
		if err != nil {
			return b, err
		}
		if b.Workspace.Pending == nil {
			b, err = controlled.workspacePreparation().PrepareAcquired(child, b, "", false)
			if err != nil {
				return b, err
			}
		}
		finished, err := controlled.workspaceLifecycle().FinishInitialization(child, b.Key, b.Lease.Token)
		if finished == nil {
			return b, err
		}
		return finished, err
	}}).Complete(ctx, b)
}

// StageWorkspaceRecoveryContext materializes only the exact sealed source
// vector, with a fresh workspace identity and retained initialization owner.
func (s Store) StageWorkspaceRecoveryContext(ctx context.Context, creationID string, spec workspace.RecoverySpec) (*Bench, error) {
	if err := workspace.ValidateRecovery(creationID, spec); err != nil {
		return nil, err
	}
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	m := workspace.Manager{State: s.State, Data: s.Data, Timeout: s.MirrorTimeout,
		Within: s.workspaceWithin, Git: s.workspaceGit, Admit: s.admit, Allocate: s.allocateWorkspace}
	if s.ProposedFreshPayload {
		m.Fresh = workspacebootstrap.New(s.State, s.Data)
	}
	return m.StageRecovery(ctx, creationID, spec)
}

func (s Store) RecoverWorkspaceContext(ctx context.Context, creationID string, spec workspace.RecoverySpec) (*Bench, error) {
	b, err := s.StageWorkspaceRecoveryContext(ctx, creationID, spec)
	if err != nil {
		return b, err
	}
	return s.finishWorkspaceInitialization(ctx, b)
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

// AcquireWorkspaceContext refreshes registered mirrors and prepares every member
// under one new owner. Preparation failure retains that owner for explicit retry.
func (s Store) AcquireWorkspaceContext(ctx context.Context, key, holder string) (*Bench, error) {
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	b, err := s.workspaceLifecycle().Acquire(ctx, key, holder)
	if err != nil {
		return b, err
	}
	return s.workspacePreparation().PrepareAcquired(ctx, b, "", false)
}

// RunWorkspaceContext executes in a registered member, or the aggregate root
// for an empty selector. The stopped owner remains held for checkpoint/release.
func (s Store) RunWorkspaceContext(ctx context.Context, key, token, selector string, args []string, offline bool, finite workspaceprep.FiniteRun) (*workspaceprep.RunResult, error) {
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	return s.workspacePreparation().Run(ctx, key, token, selector, args, offline, finite)
}

func (s Store) ReleaseWorkspaceContext(ctx context.Context, key, token string) (*Bench, error) {
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	return s.workspaceLifecycle().Release(ctx, key, token)
}

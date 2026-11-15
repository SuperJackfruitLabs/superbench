package bench

import (
	"context"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
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
	m.Within = func(fn func(workspace.Update) error) error {
		return s.session(func(session *statestore.Session) error {
			return fn(func(update workspace.Change) error { return s.update(session, update) })
		})
	}
	m.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		controlled := s
		controlled.controls = ctx
		return controlled.git(dir, args...)
	}
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

package bench

import (
	"context"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

var ErrWorkspacePreparation = workspaceprep.ErrPreparation

func (s Store) VerifyWorkspacePreparationContext(ctx context.Context, key, token string) (*Bench, error) {
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	return s.workspacePreparation().VerifyPrepared(ctx, key, token)
}

func (s Store) CompleteWorkspaceSetupContext(ctx context.Context, key, token string) (*Bench, error) {
	s, ctx, err := s.workspaceControls(ctx)
	if err != nil {
		return nil, err
	}
	return s.workspacePreparation().CompleteSetup(ctx, s.workspaceLifecycle(), key, token)
}

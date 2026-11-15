package bench

import (
	"context"
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func TestInitializationCancellationAfterStagingKeepsStoppedOwner(t *testing.T) {
	s, remote := fixture(t)
	b, err := s.StageWorkspaceContext(context.Background(), "cancel-create", workspace.Spec{Key: "staged", CheckpointRepository: "a", Repositories: []workspace.Repository{{Name: "a", Repository: remote}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := s.finishWorkspaceInitialization(ctx, b)
	if !errors.Is(err, context.Canceled) || result == nil || result.Lease == nil || result.Lease.Token != b.Lease.Token || !result.Lease.StopRequested || result.Lease.Continuation != "" {
		t.Fatal("canceled finalization discarded its owner or allowed continuation", err)
	}
	if _, err := s.PrepareWorkspaceContext(context.Background(), b.Key, b.Lease.Token, "", false); err == nil {
		t.Fatal("initialization continued after canceled handoff")
	}
}

func TestInitializationReplayCharacterization(t *testing.T) {
	for _, phase := range []string{"ready", "archived", "reaped"} {
		t.Run(phase, func(t *testing.T) {
			b := &Bench{Key: "card", Workspace: &model.Workspace{Phase: phase}}
			if phase != "ready" {
				b.Workspace.Archive = &model.WorkspaceArchive{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			got, err := (Store{}).finishWorkspaceInitialization(ctx, b)
			if err != nil || got != b {
				t.Fatal("completed replay unexpectedly touched controls or changed result", err)
			}
		})
	}
	b := &Bench{Key: "card", Workspace: &model.Workspace{Phase: "ready", Transfer: &model.WorkspaceTransfer{}}}
	if got, err := (Store{}).finishWorkspaceInitialization(context.Background(), b); got != nil || !errors.Is(err, workspace.ErrTransfer) {
		t.Fatal("transfer replay disclosed result", err)
	}
}

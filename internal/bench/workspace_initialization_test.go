package bench

import (
	"context"
	"errors"
	"testing"

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

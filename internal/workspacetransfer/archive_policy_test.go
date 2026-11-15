package workspacetransfer_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacearchive"
)

func TestTransferredTaskKeepsIntegrationBaseAndDoesNotArchiveAgainstItself(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	_, err := f.m.Begin(context.Background(), *p)
	must(t, err)
	_, err = f.m.Continue(context.Background(), "source", p.ID)
	must(t, err)
	owner, err := f.store.AcquireWorkspaceOwnerContext(context.Background(), "target", "new-work")
	must(t, err)
	for i, m := range owner.Workspace.Members {
		if m.Base != f.source.Workspace.Members[i].Base || m.Base != "main" || m.Base == m.Branch {
			t.Fatal("transfer lost original integration base")
		}
		must(t, os.MkdirAll(filepath.Join(m.Path, ".bench"), 0700))
		must(t, os.WriteFile(filepath.Join(m.Path, ".bench", "config.toml"), []byte("[archive]\nwhen='branch-merged'\n"), 0600))
		git(t, m.Path, "add", ".bench/config.toml")
		git(t, m.Path, "commit", "-m", "future-task-work")
	}
	checkpointed, err := f.store.CheckpointWorkspaceContext(context.Background(), "target", owner.Lease.Token)
	must(t, err)
	if len(checkpointed.Workspace.Unmerged) != 2 {
		t.Fatal("future task work did not record whole unmerged vector")
	}
	_, err = f.store.ReleaseWorkspaceContext(context.Background(), "target", owner.Lease.Token)
	must(t, err)
	before := f.stateBytes()
	_, err = f.store.ArchiveWorkspaceContext(context.Background(), "target", 0, func(ctx context.Context, b *bench.Bench) error {
		return workspacearchive.CheckMerged(ctx, repository.Mirrors{State: f.store.State, Data: f.store.Data}, b)
	})
	if !errors.Is(err, workspacearchive.ErrIneligible) {
		t.Fatal("unmerged transferred task archived using its own ref", err)
	}
	if f.row("target").Workspace.Archive != nil || !bytes.Equal(before, f.stateBytes()) {
		t.Fatal("ineligible archive persisted intent")
	}
}

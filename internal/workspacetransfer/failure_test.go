package workspacetransfer_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
)

func TestPartialDetachRetainsIrreversibleApprovalAndExactRetry(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	_, err := f.m.Begin(context.Background(), *p)
	must(t, err)
	b := p.Members[1].Worktree
	head := read(t, filepath.Join(b.Admin, "HEAD"))
	must(t, os.WriteFile(filepath.Join(b.Admin, "HEAD"), []byte("ref: refs/heads/unrelated\n"), 0600))
	out, err := f.m.Continue(context.Background(), "source", p.ID)
	if err == nil || out == nil || out.Phase != "started" || out.GrantToken != "" {
		t.Fatal("partial detach did not preserve started approval", err)
	}
	if got := string(read(t, filepath.Join(p.Members[0].Worktree.Admin, "HEAD"))); got != p.Members[0].Worktree.Commit+"\n" {
		t.Fatal("first member was not detached before second refusal")
	}
	if f.row("target") != nil || f.row("source").Workspace.Transfer.Phase != "relinquishing" || f.journal(p.ID).Phase != "started" {
		t.Fatal("partial detach published ownership or lost source tombstone")
	}
	must(t, os.WriteFile(filepath.Join(b.Admin, "HEAD"), head, 0600))
	out, err = f.m.Continue(context.Background(), "source", p.ID)
	must(t, err)
	if out.Phase != "completed" || out.GrantToken != "" {
		t.Fatal("exact partial retry failed")
	}
}

func TestChangedReviewAndDestinationCollisionsRefuseBeforeAuthorityOrDetach(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	checks := []struct {
		name   string
		change func() func()
	}{
		{"ignored-source-bytes", func() func() {
			path := filepath.Join(f.source.Workspace.Members[1].Path, "private-output")
			old := read(t, path)
			must(t, os.WriteFile(path, []byte("changed"), 0600))
			return func() { must(t, os.WriteFile(path, old, 0600)) }
		}},
		{"tracked-source-bytes", func() func() {
			path := filepath.Join(f.source.Workspace.Members[1].Path, "source.txt")
			old := read(t, path)
			must(t, os.WriteFile(path, []byte("changed"), 0600))
			return func() { must(t, os.WriteFile(path, old, 0600)) }
		}},
		{"target-files", func() func() {
			path := filepath.Join(f.store.Data, "benches", "target")
			must(t, os.Mkdir(path, 0700))
			return func() { must(t, os.Remove(path)) }
		}},
		{"legacy-target-intent", func() func() {
			path := filepath.Join(f.store.State, "create-target.json")
			must(t, os.WriteFile(path, []byte("preserve"), 0600))
			return func() { must(t, os.Remove(path)) }
		}},
		{"local-task-tip", func() func() {
			m := f.source.Workspace.Members[1]
			tree := git(t, m.Path, "rev-parse", "HEAD^{tree}")
			next := git(t, m.Path, "commit-tree", tree, "-p", m.Source, "-m", "other-tip")
			git(t, m.Mirror, "update-ref", "refs/heads/"+m.Branch, next, m.Source)
			return func() { git(t, m.Mirror, "update-ref", "refs/heads/"+m.Branch, m.Source, next) }
		}},
		{"remote-task-tip", func() func() {
			m := f.source.Workspace.Members[1]
			tree := git(t, m.Repository, "rev-parse", m.Source+"^{tree}")
			next := git(t, m.Repository, "commit-tree", tree, "-p", m.Source, "-m", "remote-tip")
			git(t, m.Repository, "update-ref", "refs/heads/"+m.Branch, next, m.Source)
			return func() { git(t, m.Repository, "update-ref", "refs/heads/"+m.Branch, m.Source, next) }
		}},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			restore := c.change()
			defer restore()
			before := f.stateBytes()
			out, err := f.m.Begin(context.Background(), *p)
			if err == nil || out != nil {
				t.Fatal("changed approval accepted", err)
			}
			if !bytes.Equal(before, f.stateBytes()) {
				t.Fatal("refused approval wrote authority")
			}
			for _, m := range f.source.Workspace.Members {
				if got := git(t, m.Path, "symbolic-ref", "HEAD"); got != "refs/heads/"+m.Branch {
					t.Fatal("refused approval detached source")
				}
			}
		})
	}
	if f.row("source").Workspace.Transfer != nil || f.row("target") != nil {
		t.Fatal("failed review left ownership changes")
	}
	err := f.sql(func(tx *sql.Tx) error {
		_, e := reftransfer.Get(tx, p.ID)
		if !errors.Is(e, sql.ErrNoRows) {
			t.Fatal("refused review created journal", e)
		}
		return nil
	})
	must(t, err)
}

func TestCancelledOrWrongSourceContinuationCannotChangeApprovedState(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := f.stateBytes()
	if _, err := f.m.Begin(ctx, *p); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled begin accepted", err)
	}
	if !bytes.Equal(before, f.stateBytes()) {
		t.Fatal("cancelled begin wrote authority")
	}
	_, err := f.m.Begin(context.Background(), *p)
	must(t, err)
	started := f.stateBytes()
	if out, err := f.m.Continue(ctx, "source", p.ID); !errors.Is(err, context.Canceled) || out != nil && out.GrantToken != "" {
		t.Fatal("cancelled continuation granted", err)
	}
	if out, err := f.m.Continue(context.Background(), "different", p.ID); err == nil || out != nil {
		t.Fatal("wrong source resumed transfer", err)
	}
	if !bytes.Equal(started, f.stateBytes()) {
		t.Fatal("unauthorized continuation changed state")
	}
}

func TestSourceOwnershipAcquiredAfterReviewInvalidatesApprovalWithoutLeakingToken(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	owner, err := f.store.AcquireWorkspaceOwnerContext(context.Background(), "source", "existing-owner")
	must(t, err)
	before := f.stateBytes()
	out, err := f.m.Begin(context.Background(), *p)
	if err == nil || out != nil || !bytes.Equal(before, f.stateBytes()) || f.row("source").Lease.Token != owner.Lease.Token {
		t.Fatal("stale review changed or exposed newly acquired owner", err)
	}
	if f.row("target") != nil || f.row("source").Workspace.Transfer != nil {
		t.Fatal("stale owner approval left transfer intent")
	}
}

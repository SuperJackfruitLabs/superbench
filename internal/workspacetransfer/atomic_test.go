package workspacetransfer_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func TestDestinationRegistrationFailureKeepsSourceRelinquishedAndBindsRawStagingApproval(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	_, err := f.m.Begin(context.Background(), *p)
	must(t, err)
	must(t, f.sql(func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER refuse_destination BEFORE INSERT ON benches WHEN NEW.key='target' BEGIN SELECT RAISE(ABORT,'injected destination save failure'); END")
		return err
	}))
	out, err := f.m.Continue(context.Background(), "source", p.ID)
	if err == nil || out == nil || out.Phase != "relinquished" || out.GrantToken != "" || f.row("target") != nil || f.row("source").Workspace.Transfer.Phase != "transferred" {
		t.Fatal("failed target save lost durable relinquishment", err)
	}
	must(t, f.sql(func(tx *sql.Tx) error { _, err := tx.Exec("DROP TRIGGER refuse_destination"); return err }))
	spec := workspace.Spec{TransferID: p.ID, Key: p.TargetKey, CheckpointRepository: p.CheckpointRepository, Repositories: p.Repositories, QuotaBytes: p.QuotaBytes}
	for name, change := range map[string]func(*workspace.Spec){"quota": func(s *workspace.Spec) { s.QuotaBytes *= 2 }, "checkpoint-repository": func(s *workspace.Spec) { s.CheckpointRepository = "b" }, "original-ref": func(s *workspace.Spec) {
		s.Repositories = append([]workspace.Repository(nil), s.Repositories...)
		s.Repositories[1].Branch = "bench/unreviewed"
	}, "base": func(s *workspace.Spec) {
		s.Repositories = append([]workspace.Repository(nil), s.Repositories...)
		s.Repositories[1].Base = "other"
	}} {
		t.Run(name, func(t *testing.T) {
			changed := spec
			change(&changed)
			before := f.stateBytes()
			b, err := f.store.StageWorkspaceContext(context.Background(), p.CreationID, changed)
			if err == nil || b != nil {
				t.Fatal("raw staging bypassed reviewed request", err)
			}
			if !bytes.Equal(before, f.stateBytes()) {
				t.Fatal("raw rejected request created intent")
			}
			if _, err := os.Lstat(filepath.Join(f.store.Data, "benches", "target")); !os.IsNotExist(err) {
				t.Fatal("raw rejected request allocated destination", err)
			}
		})
	}
	// A relinquished tombstone does not authorize adopting later task tips.
	member := f.source.Workspace.Members[1]
	tree := git(t, member.Repository, "rev-parse", member.Source+"^{tree}")
	next := git(t, member.Repository, "commit-tree", tree, "-p", member.Source, "-m", "later-remote-tip")
	git(t, member.Repository, "update-ref", "refs/heads/"+member.Branch, next, member.Source)
	beforeTipRefusal := f.stateBytes()
	out, err = f.m.Continue(context.Background(), "source", p.ID)
	if err == nil || out == nil || out.GrantToken != "" || f.row("target") != nil || !bytes.Equal(beforeTipRefusal, f.stateBytes()) {
		t.Fatal("relinquished retry adopted later remote task tip", err)
	}
	git(t, member.Repository, "update-ref", "refs/heads/"+member.Branch, member.Source, next)
	out, err = f.m.Continue(context.Background(), "source", p.ID)
	must(t, err)
	if out.Phase != "completed" || out.GrantToken != "" {
		t.Fatal("retry could not complete immutable approved target")
	}
}

func TestCompletionJournalFailureRetainsReadyDestinationAndExactRetry(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	_, err := f.m.Begin(context.Background(), *p)
	must(t, err)
	must(t, f.sql(func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER refuse_completion BEFORE UPDATE ON workspace_ref_transfers WHEN NEW.phase='completed' BEGIN SELECT RAISE(ABORT,'injected completion save failure'); END")
		return err
	}))
	out, err := f.m.Continue(context.Background(), "source", p.ID)
	if err == nil || out == nil || out.Phase != "relinquished" || out.GrantToken != "" {
		t.Fatal("failed completion fabricated success or grant", err)
	}
	target := f.row("target")
	if target == nil || target.Lease != nil || target.Workspace.Phase != "ready" || target.Workspace.Checkpoint == nil || f.row("source").Workspace.Transfer.Phase != "transferred" {
		t.Fatal("journal failure lost independently committed readiness")
	}
	// A reserved-looking holder name is still an ordinary later acquisition.
	owner, err := f.store.AcquireWorkspaceOwnerContext(context.Background(), "target", "initialization")
	must(t, err)
	held := f.stateBytes()
	out, err = f.m.Continue(context.Background(), "source", p.ID)
	if err == nil || out == nil || out.GrantToken != "" || out.RootPath != "" {
		t.Fatal("incomplete journal replay leaked unrelated ready-phase owner", err)
	}
	if !bytes.Equal(held, f.stateBytes()) || f.row("target").Lease.Token != owner.Lease.Token {
		t.Fatal("incomplete journal replay replaced unrelated owner")
	}
	_, err = f.store.ReleaseWorkspaceContext(context.Background(), "target", owner.Lease.Token)
	must(t, err)
	must(t, f.sql(func(tx *sql.Tx) error { _, err := tx.Exec("DROP TRIGGER refuse_completion"); return err }))
	before := target.Workspace.Checkpoint.Seal
	out, err = f.m.Continue(context.Background(), "source", p.ID)
	must(t, err)
	if out.Phase != "completed" || out.GrantToken != "" || f.row("target").Workspace.Checkpoint.Seal != before {
		t.Fatal("completion retry recreated destination genesis")
	}
}

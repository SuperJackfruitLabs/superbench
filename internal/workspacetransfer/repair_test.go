package workspacetransfer_test

import (
	"bytes"
	"context"
	"database/sql"
	"sync"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func TestStoppedTransferredInitializationRequiresExplicitRepair(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	_, err := f.m.Begin(context.Background(), *p)
	must(t, err)
	must(t, f.sql(func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER defer_target BEFORE INSERT ON benches WHEN NEW.key='target' BEGIN SELECT RAISE(ABORT,'defer destination'); END")
		return err
	}))
	if _, err = f.m.Continue(context.Background(), "source", p.ID); err == nil {
		t.Fatal("injected target failure ignored")
	}
	must(t, f.sql(func(tx *sql.Tx) error { _, err := tx.Exec("DROP TRIGGER defer_target"); return err }))
	spec := workspace.Spec{TransferID: p.ID, Key: p.TargetKey, CheckpointRepository: p.CheckpointRepository, Repositories: p.Repositories, QuotaBytes: p.QuotaBytes}
	target, err := f.store.StageWorkspaceContext(context.Background(), p.CreationID, spec)
	must(t, err)
	old := target.Lease.Token
	_, err = f.store.StopWorkspace("target", old, false)
	must(t, err)
	stopped := f.stateBytes()
	out, err := f.m.Continue(context.Background(), "source", p.ID)
	if err == nil || out == nil || out.GrantToken != "" || !out.StopRequested {
		t.Fatal("ordinary transfer retry revived stopped initialization", err)
	}
	if !bytes.Equal(stopped, f.stateBytes()) {
		t.Fatal("stopped transfer retry changed evidence")
	}
	if target, err := f.store.FinishWorkspaceInitializationContext(context.Background(), "target", old); err == nil || target != nil && target.Lease == nil {
		t.Fatal("raw finish bypassed stopped owner")
	}
	admin, err := f.store.WorkspaceRepair()
	must(t, err)
	repair, err := admin.Inspect(context.Background(), "target", nil)
	must(t, err)
	claimed, err := admin.Claim(context.Background(), *repair, "target-repair")
	must(t, err)
	if claimed.GrantToken == "" || claimed.GrantToken == old {
		t.Fatal("explicit repair did not replace stopped owner")
	}
	repaired, err := admin.Continue(context.Background(), "target", "target-repair", claimed.GrantToken)
	must(t, err)
	if repaired.Phase != "ready" || repaired.GrantToken != "" {
		t.Fatal("explicit repaired target not ready")
	}
	out, err = f.m.Continue(context.Background(), "source", p.ID)
	must(t, err)
	if out.Phase != "completed" || out.GrantToken != "" || f.row("source").Workspace.Transfer.Phase != "transferred" {
		t.Fatal("transfer failed to acknowledge independent repaired readiness")
	}
}

func TestConcurrentDifferentApprovalsHaveExactlyOneIrreversibleWinner(t *testing.T) {
	f := newFixture(t)
	first := f.plan()
	second := f.plan()
	type response struct{ err error }
	results := make(chan response, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	// The approvals share a request ID but independently selected target identities.
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			p := first
			if i == 1 {
				p = second
			}
			_, err := f.m.Begin(context.Background(), *p)
			results <- response{err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for r := range results {
		if r.err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("concurrent approvals did not select one exact winner", success)
	}
	j := f.journal(first.ID)
	if j.Phase != "started" || (j.Digest != first.Digest && j.Digest != second.Digest) {
		t.Fatal("winner not durably bound")
	}
	if f.row("source").Workspace.Transfer.Digest != j.Digest || f.row("target") != nil {
		t.Fatal("concurrent claims split journal and source authority")
	}
	out, err := f.m.Continue(context.Background(), "source", first.ID)
	must(t, err)
	if out.Phase != "completed" || out.TargetID != j.TargetID {
		t.Fatal("continuation did not use exact winning identity")
	}
}

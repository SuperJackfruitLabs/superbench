package crosshostflow

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func TestImportRemoteMismatchRetainsExactIntentWithoutAllocationAndResumes(t *testing.T) {
	f, m, a := incomingFixture(t)
	ctx := context.Background()
	r := f.ready.Workspace.Members[1]
	git(t, r.Repository, "update-ref", "refs/heads/"+r.Branch, git(t, r.Repository, "rev-parse", "main"))
	// Create a genuine different commit so the remote task ref no longer matches.
	git(t, r.Repository, "commit", "--allow-empty", "-m", "remote advance")
	advanced := git(t, r.Repository, "rev-parse", "HEAD")
	git(t, r.Repository, "update-ref", "refs/heads/"+r.Branch, advanced)
	out, err := m.Import(ctx, a)
	if err == nil || out == nil || out.Phase != "accepted" || out.GrantToken != "" || out.WorkspaceID != a.Destination.WorkspaceID {
		t.Fatal("failed preflight lost exact retained import", out, err)
	}
	if _, err = os.Lstat(filepath.Join(f.dest.Data, "benches", f.spec.Key)); !os.IsNotExist(err) {
		t.Fatal("remote mismatch allocated checkout", err)
	}
	git(t, r.Repository, "update-ref", "refs/heads/"+r.Branch, a.Members[1].Commit)
	// The failed admission observed and retained the divergent local mirror ref.
	// Refresh never rewrites it implicitly; an operator must review/restore it.
	if _, err = m.ContinueImport(ctx, f.spec.Key, a.Destination.CreationID); err == nil {
		t.Fatal("retry silently rewrote a divergent local mirror ref")
	}
	git(t, repository.Path(f.dest.Data, r.Repository), "update-ref", "refs/heads/"+r.Branch, a.Members[1].Commit, advanced)
	ready, err := m.ContinueImport(ctx, f.spec.Key, a.Destination.CreationID)
	must(t, err)
	if ready.Phase != "completed" || ready.WorkspaceID != a.Destination.WorkspaceID {
		t.Fatal("retry selected a new import identity")
	}
}

func TestFinalImportSaveFailureRollsBackReadyAndJournalThenRetriesSameOwner(t *testing.T) {
	f, m, a := incomingFixture(t)
	ctx := context.Background()
	must(t, sqlChange(t, f.dest.State, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER stop_ready BEFORE UPDATE ON benches WHEN json_extract(NEW.record,'$.Workspace.Phase')='ready' BEGIN SELECT RAISE(IGNORE); END`)
		return err
	}))
	retained, err := m.Import(ctx, a)
	if err == nil || retained == nil || retained.GrantToken == "" || retained.Phase != "accepted" {
		t.Fatal("readiness failure lost original owner", retained, err)
	}
	before := snapshot(t, f.dest, f.spec.Key)
	if before.Workspace.Phase != "prepared" || before.Lease == nil || before.Workspace.Checkpoint == nil {
		t.Fatal("final failure damaged prepared checkpoint/lease")
	}
	must(t, sqlChange(t, f.dest.State, func(tx *sql.Tx) error {
		i, err := handoffjournal.ImportByID(tx, a.Destination.CreationID)
		if err != nil {
			return err
		}
		if i.Phase != "accepted" {
			t.Fatal("completed journal survived failed core save")
		}
		_, err = tx.Exec(`DROP TRIGGER stop_ready`)
		return err
	}))
	completed, err := m.ContinueImport(ctx, f.spec.Key, a.Destination.CreationID)
	must(t, err)
	if completed.Phase != "completed" || completed.GrantToken != "" {
		t.Fatal("import did not settle exact retained owner")
	}
	after := snapshot(t, f.dest, f.spec.Key)
	if after.Workspace.ID != before.Workspace.ID || after.Workspace.Checkpoint.GenerationID != before.Workspace.Checkpoint.GenerationID {
		t.Fatal("retry replaced workspace/checkpoint generation")
	}
}

func TestFailedDetachSQLSaveRetainsExcludedSourceAndRetryDoesNotRedirect(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	source, dest := Manager{Store: f.source}, Manager{Store: f.dest}
	o, err := dest.Offer(ctx, f.spec.Key, "import-create", 1<<20)
	must(t, err)
	o.Destination.HostID = strings.Repeat("e", 64)
	p, err := source.InspectExport(ctx, f.spec.Key, "export", *o)
	must(t, err)
	_, err = source.BeginExport(ctx, *p)
	must(t, err)
	must(t, sqlChange(t, f.source.State, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER stop_transferred BEFORE UPDATE ON benches WHEN json_extract(NEW.record,'$.Workspace.Transfer.Phase')='transferred' BEGIN SELECT RAISE(IGNORE); END`)
		return err
	}))
	if _, err = source.ContinueExport(ctx, f.spec.Key, "export"); err == nil {
		t.Fatal("ignored final source phase acknowledged")
	}
	b := snapshot(t, f.source, f.spec.Key)
	if b.Workspace.Transfer.Phase != "relinquishing" {
		t.Fatal("lost permanent source exclusion")
	}
	for _, r := range b.Workspace.Members {
		if git(t, r.Path, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" {
			t.Fatal("fixture did not detach source")
		}
	}
	must(t, sqlChange(t, f.source.State, func(tx *sql.Tx) error { _, err := tx.Exec(`DROP TRIGGER stop_transferred`); return err }))
	exported, err := source.ContinueExport(ctx, f.spec.Key, "export")
	must(t, err)
	if exported.Destination != o.Destination {
		t.Fatal("failed detached retry redirected destination")
	}
}

func TestStoppedImportRequiresExplicitRepairAndCompletionKeepsOriginalImport(t *testing.T) {
	f, m, a := incomingFixture(t)
	ctx := context.Background()
	must(t, sqlChange(t, f.dest.State, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER stop_ready BEFORE UPDATE ON benches WHEN json_extract(NEW.record,'$.Workspace.Phase')='ready' BEGIN SELECT RAISE(ABORT,'hold prepared'); END`)
		return err
	}))
	held, err := m.Import(ctx, a)
	if err == nil || held == nil || held.GrantToken == "" {
		t.Fatal("fixture did not retain initialization owner", err)
	}
	_, err = f.dest.StopWorkspace(f.spec.Key, held.GrantToken, false)
	must(t, err)
	blocked, err := m.ContinueImport(ctx, f.spec.Key, a.Destination.CreationID)
	if err == nil || blocked == nil || blocked.GrantToken != "" {
		t.Fatal("stopped import revived owner", blocked, err)
	}
	must(t, sqlChange(t, f.dest.State, func(tx *sql.Tx) error { _, err := tx.Exec(`DROP TRIGGER stop_ready`); return err }))
	admin, err := f.dest.WorkspaceRepair()
	must(t, err)
	p, err := admin.Inspect(ctx, f.spec.Key, nil)
	must(t, err)
	claimed, err := admin.Claim(ctx, *p, "explicit-repair")
	must(t, err)
	if claimed.GrantToken == "" || claimed.GrantToken == held.GrantToken {
		t.Fatal("repair did not preserve a distinct explicit epoch")
	}
	_, err = admin.Continue(ctx, f.spec.Key, "explicit-repair", claimed.GrantToken)
	must(t, err)
	completed, err := m.ContinueImport(ctx, f.spec.Key, a.Destination.CreationID)
	must(t, err)
	if completed.Phase != "completed" || completed.GrantToken != "" || completed.WorkspaceID != a.Destination.WorkspaceID {
		t.Fatal("repair completion rebound original import", completed)
	}
}

func TestChangedLaterSourceAfterBeginRefusesBeforeFirstDetach(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	source, dest := Manager{Store: f.source}, Manager{Store: f.dest}
	file := filepath.Join(f.ready.Workspace.Members[1].Path, "private-output")
	must(t, os.WriteFile(file, []byte("approved"), 0600))
	o, err := dest.Offer(ctx, f.spec.Key, "import-create", 1<<20)
	must(t, err)
	o.Destination.HostID = strings.Repeat("e", 64)
	p, err := source.InspectExport(ctx, f.spec.Key, "export", *o)
	must(t, err)
	_, err = source.BeginExport(ctx, *p)
	must(t, err)
	must(t, os.WriteFile(file, []byte("changed"), 0600))
	if _, err = source.ContinueExport(ctx, f.spec.Key, "export"); err == nil {
		t.Fatal("changed later source allowed detachment")
	}
	if git(t, f.ready.Workspace.Members[0].Path, "symbolic-ref", "HEAD") != "refs/heads/card/same" {
		t.Fatal("first member detached before whole-vector preflight")
	}
	if snapshot(t, f.source, f.spec.Key).Workspace.Transfer.Phase != "relinquishing" {
		t.Fatal("failed validation restored source authority")
	}
	must(t, os.WriteFile(file, []byte("approved"), 0600))
	_, err = source.ContinueExport(ctx, f.spec.Key, "export")
	must(t, err)
}

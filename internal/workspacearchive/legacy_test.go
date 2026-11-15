package workspacearchive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

// This is the exact schema20 approval shape, before transfer/admin identities.
// Constructing it independently prevents new Plan fields altering saved bytes.
type schema20Plan struct {
	Version, Key, Path, BenchID string
	Checkpoint                  *model.WorkspaceCheckpoint
	Members                     []model.WorkspaceMember
	FreezeDigest, ReapAfter     string
	Tree                        reap.Tree
	Digest                      string
}

func TestSchema20RetainedOrdinaryReapMigratesAndRetriesExactJournal(t *testing.T) {
	f := newFixture(t)
	b := f.archive(t)
	current, err := f.manager.PlanReap(context.Background(), b.Key)
	if err != nil {
		t.Fatal(err)
	}
	old := schema20Plan{Version: current.Version, Key: current.Key, Path: current.Path, BenchID: current.BenchID, Checkpoint: current.Checkpoint, Members: current.Members, FreezeDigest: current.FreezeDigest, ReapAfter: current.ReapAfter, Tree: current.Tree}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	old.Digest = hex.EncodeToString(sum[:])
	raw, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := ReadPlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(approval.Registrations) != 0 || approval.Transfer != nil {
		t.Fatal("legacy approval gained new authority")
	}
	roundtrip, err := approval.Bytes()
	if err != nil || !bytes.Equal(roundtrip, raw) {
		t.Fatal("legacy bytes/digest changed", err)
	}
	f.change(t, func(b *model.Bench) { b.Workspace.Phase = "reaping"; b.Workspace.Archive.ReapingDigest = old.Digest })
	if err = os.WriteFile(f.manager.reapJournal(b.Key), raw, 0600); err != nil {
		t.Fatal(err)
	}
	// The prior binary had committed approval and removed checkout contents, but
	// registration cleanup and quota retirement had not completed.
	if err = reap.DeleteFrozenContents(b.Path, old.Tree); err != nil {
		t.Fatal(err)
	}
	if err = statestore.Within(f.manager.State, nil, func(s *statestore.Session) error {
		return s.Update(func(tx *sql.Tx) error {
			if _, e := tx.Exec("DROP TABLE workspace_ref_transfers"); e != nil {
				return e
			}
			_, e := tx.Exec("PRAGMA user_version=20")
			return e
		})
	}); err != nil {
		t.Fatal(err)
	}
	reaped, err := f.manager.Reap(context.Background(), *approval)
	if err != nil {
		t.Fatal("legacy retry after migration", err)
	}
	if reaped.Workspace.Phase != "reaped" {
		t.Fatal("legacy reap incomplete")
	}
	saved, err := os.ReadFile(f.manager.reapJournal(b.Key))
	if err != nil || !bytes.Equal(saved, raw) {
		t.Fatal("migration rewrote approval", err)
	}
	db, err := statestoreOpen(f.manager.State)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 22 {
		t.Fatal("legacy state not migrated", version, err)
	}
	for _, m := range b.Workspace.Members {
		out := gitOK(t, m.Mirror, "worktree", "list", "--porcelain")
		if bytes.Contains([]byte(out), []byte(m.Path)) {
			t.Fatal("legacy registration remains")
		}
		if gitOK(t, m.Mirror, "rev-parse", "refs/heads/"+m.Branch) == "" {
			t.Fatal("legacy ref deleted")
		}
	}
}

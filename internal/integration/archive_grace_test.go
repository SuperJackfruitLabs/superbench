package integration

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"

	_ "modernc.org/sqlite"
)

func graceFixture(t *testing.T, grace time.Duration) (bench.Store, *bench.Bench) {
	t.Helper()
	s, _, _, _ := fixture(t)
	l, e := s.Lease("task", "writer")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Checkpoint("task", l.Lease.Token); e != nil {
		t.Fatal(e)
	}
	if e = s.Release("task", l.Lease.Token); e != nil {
		t.Fatal(e)
	}
	b, e := s.ArchiveWithGrace("task", grace)
	if e != nil {
		t.Fatal(e)
	}
	return s, b
}

func legacyArchive(t *testing.T, s bench.Store, b *bench.Bench) {
	t.Helper()
	b.ReapAfter = ""
	b.ArchiveFreezeDigest, b.ArchiveFrozenAt = "", ""
	raw, e := json.Marshal(b)
	if e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", filepath.Join(s.State, "bench.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec("UPDATE benches SET record=? WHERE key=?", string(raw), b.Key); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("DROP TABLE workspace_operation_cancellations; DROP TABLE workspace_operation_runs; DROP TABLE workspace_operations; PRAGMA user_version=8"); e != nil {
		t.Fatal(e)
	}
}

func TestArchiveGraceCannotBeShortenedByRetryOrPlan(t *testing.T) {
	s, b := graceFixture(t, 7*24*time.Hour)
	p, e := s.PlanReap("task")
	if e != nil || p.Version != "superbench.reap.v3" || p.ReapAfter != b.ReapAfter {
		t.Fatal(p, e)
	}
	if _, e = s.Reap(*p); e == nil {
		t.Fatal("grace bypassed")
	}
	if _, e = os.Stat(filepath.Join(b.Path, "file")); e != nil {
		t.Fatal("premature reap removed data", e)
	}
	if _, e = os.Stat(filepath.Join(s.State, "reap-task.json")); !os.IsNotExist(e) {
		t.Fatal("premature intent published", e)
	}
	again, e := s.ArchiveWithGrace("task", 0)
	if e != nil || again.ReapAfter != b.ReapAfter {
		t.Fatal("retry shortened retained deadline", again, e)
	}
	forged := *p
	forged.ReapAfter = b.ArchivedAt
	forged.Digest, _ = forged.DigestValue()
	if _, e = s.Reap(forged); e == nil {
		t.Fatal("caller shortened deadline")
	}
	legacy := *p
	legacy.Version, legacy.ReapAfter, legacy.FreezeDigest = "superbench.reap.v1", "", ""
	legacy.Digest, _ = legacy.DigestValue()
	if _, e = s.Reap(legacy); e == nil {
		t.Fatal("fresh legacy approval bypassed grace")
	}
}

func TestArchiveMigrationPinsOriginalTimeAndRequiresNewApproval(t *testing.T) {
	s, b := graceFixture(t, 0)
	p, e := s.PlanReap("task")
	if e != nil {
		t.Fatal(e)
	}
	p.Version, p.ReapAfter, p.FreezeDigest = "superbench.reap.v1", "", ""
	p.Digest, _ = p.DigestValue()
	at := time.Now().UTC().Add(-10 * 24 * time.Hour)
	b.ArchivedAt = at.Format(time.RFC3339Nano)
	legacyArchive(t, s, b)
	if _, e = s.Status("task"); e == nil {
		t.Fatal("read-only status silently upgraded state")
	}
	if e = s.Migrate(); e != nil {
		t.Fatal(e)
	}
	states, e := s.Status("task")
	if e != nil || states[0].ReapAfter != at.Add(7*24*time.Hour).Format(time.RFC3339Nano) {
		t.Fatal(states, e)
	}
	if _, e = s.Reap(*p); e == nil {
		t.Fatal("unstarted v1 approval was reused")
	}
	if _, e = s.Archive("task"); e != nil {
		t.Fatal(e)
	}
	fresh, e := s.PlanReap("task")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Reap(*fresh); e != nil {
		t.Fatal("eligible migrated archive did not reap", e)
	}
}

func TestAlreadyApprovedLegacyReapRemainsRecoverable(t *testing.T) {
	s, b := graceFixture(t, 0)
	p, e := s.PlanReap("task")
	if e != nil {
		t.Fatal(e)
	}
	p.Version, p.ReapAfter, p.FreezeDigest = "superbench.reap.v1", "", ""
	p.Digest, _ = p.DigestValue()
	if e = filepath.WalkDir(b.Path, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		return os.Chmod(path, st.Mode()|0200)
	}); e != nil {
		t.Fatal(e)
	}
	p.Tree, e = reap.Snapshot(b.Path, nil)
	if e != nil {
		t.Fatal(e)
	}
	p.Digest, _ = p.DigestValue()
	b.ReapingDigest = p.Digest
	legacyArchive(t, s, b)
	raw, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(s.State, "reap-task.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(); e != nil {
		t.Fatal(e)
	}
	// The exact v1 intent was committed before upgrade. The new default grace
	// cannot strand its already-approved partial deletion or widen the manifest.
	write(t, filepath.Join(b.Path, "new-work"), "keep")
	if _, e = s.Reap(*p); e == nil {
		t.Fatal("old intent deleted new work")
	}
	if e = os.Remove(filepath.Join(b.Path, "new-work")); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Reap(*p); e != nil {
		t.Fatal("old approved intent stranded", e)
	}
	if _, e = s.Reap(*p); e != nil {
		t.Fatal("completed legacy replay failed", e)
	}
}

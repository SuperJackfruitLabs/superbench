package integration

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

func beforeArchive(t *testing.T) (bench.Store, *bench.Bench) {
	t.Helper()
	s, b, _, _ := fixture(t)
	write(t, filepath.Join(b.Path, "ignored"), "warm output")
	l, e := s.Lease("task", "worker")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Checkpoint("task", l.Lease.Token); e != nil {
		t.Fatal(e)
	}
	if e = s.Release("task", l.Lease.Token); e != nil {
		t.Fatal(e)
	}
	states, e := s.Status("task")
	if e != nil {
		t.Fatal(e)
	}
	return s, &states[0]
}

func TestArchiveDoesNotChangePermissionsBeforeDurableIntent(t *testing.T) {
	s, b := beforeArchive(t)
	if e := os.Mkdir(filepath.Join(s.State, "archive-freeze-task.json"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Archive("task"); e == nil {
		t.Fatal("unwritable intent accepted")
	}
	states, e := s.Status("task")
	if e != nil || states[0].ArchivedAt != "" || states[0].ArchiveFreezeDigest != "" {
		t.Fatal(states, e)
	}
	info, e := os.Stat(filepath.Join(b.Path, "file"))
	if e != nil || info.Mode().Perm()&0200 == 0 {
		t.Fatal("file frozen before durable intent", e)
	}
}

func TestPendingArchiveBlocksAcquisitionAndPreservesChangedFiles(t *testing.T) {
	s, b := beforeArchive(t)
	plan, e := reap.NewFreeze(b.Key, b.Path, b.Checkpoint)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := plan.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(s.State, "archive-freeze-task.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	b.ArchivedAt, b.ReapAfter = now.Format(time.RFC3339Nano), now.Add(7*24*time.Hour).Format(time.RFC3339Nano)
	b.ArchiveFreezeDigest = plan.Digest
	raw, e = json.Marshal(b)
	if e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", filepath.Join(s.State, "bench.db"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec("UPDATE benches SET record=? WHERE key=?", string(raw), b.Key)
	db.Close()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Lease("task", "competitor"); !errors.Is(e, bench.ErrArchived) {
		t.Fatal("pending archive admitted a writer", e)
	}
	if _, e = s.PlanReap("task"); e == nil {
		t.Fatal("pending archive admitted deletion planning")
	}
	write(t, filepath.Join(b.Path, "new-work"), "keep")
	pending, e := s.Archive("task")
	if e == nil || pending.ArchiveFrozenAt != "" {
		t.Fatal("changed archive silently frozen", pending, e)
	}
	if raw, e = os.ReadFile(filepath.Join(b.Path, "new-work")); e != nil || string(raw) != "keep" {
		t.Fatal(e)
	}
	// The operator preserves the conflicting new file outside the frozen tree;
	// the retry never moves, deletes or auto-commits it on the operator's behalf.
	if e = os.Rename(filepath.Join(b.Path, "new-work"), filepath.Join(filepath.Dir(b.Path), "preserved-work")); e != nil {
		t.Fatal(e)
	}
	frozen, e := s.ArchiveWithGrace("task", 0)
	if e != nil || frozen.ArchiveFrozenAt == "" || frozen.ReapAfter != b.ReapAfter {
		t.Fatal(frozen, e)
	}
	if _, e = s.PlanReap("task"); e != nil {
		t.Fatal(e)
	}
}

func TestArchivePlansDistinguishHiddenBytesAndRefuseLaterMutation(t *testing.T) {
	s, b := beforeArchive(t)
	git(t, b.Path, "update-index", "--assume-unchanged", "file")
	write(t, filepath.Join(b.Path, "file"), "hidden uncommitted source")
	if _, e := s.ArchiveWithGrace("task", 0); e != nil {
		t.Fatal(e)
	}
	p, e := s.PlanReap("task")
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, entry := range p.Tree.Entries {
		if entry.Path == "file" {
			found = true
			if entry.Recoverable {
				t.Fatal("hidden edits called recoverable")
			}
		}
	}
	if !found {
		t.Fatal("hidden file missing from review")
	}
	if e = os.Chmod(filepath.Join(b.Path, "file"), 0600); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(b.Path, "file"), "changed after freeze")
	if _, e = s.PlanReap("task"); e == nil {
		t.Fatal("fresh plan adopted a changed archive")
	}
	if _, e = s.Reap(*p); e == nil {
		t.Fatal("old approval deleted changed archive")
	}
}

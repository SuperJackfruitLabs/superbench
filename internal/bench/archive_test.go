package bench

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func archivedFixture(t *testing.T) (Store, *Bench) {
	t.Helper()
	s, r := fixture(t)
	b, e := s.Create("task", Options{Repo: r}, false)
	if e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(b.Path, ".gitignore"), "build/\n")
	g(t, b.Path, "add", ".")
	g(t, b.Path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "ignore artifacts")
	lease, e := s.Lease("task", "worker")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Checkpoint("task", lease.Lease.Token); e != nil {
		t.Fatal(e)
	}
	if e = s.Release("task", lease.Lease.Token); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(filepath.Join(b.Path, "build"), 0700); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(b.Path, "build", "non-recoverable"), "retained artifact")
	b, e = s.Archive("task")
	if e != nil {
		t.Fatal(e)
	}
	return s, b
}
func TestArchiveAndReviewedReap(t *testing.T) {
	s, b := archivedFixture(t)
	if b.ArchivedAt == "" {
		t.Fatal("archive not retained")
	}
	if _, e := s.Lease("task", "other"); !errors.Is(e, ErrArchived) {
		t.Fatal("archived lease allowed", e)
	}
	p, e := s.PlanReap("task")
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, v := range p.Tree.Entries {
		if v.Path == filepath.Join("build", "non-recoverable") {
			found = !v.Recoverable
		}
	}
	if !found {
		t.Fatal("ignored artifact missing from review")
	}
	bad := *p
	bad.Digest = "wrong"
	if _, e = s.Reap(bad); e == nil {
		t.Fatal("invalid approval accepted")
	}
	if _, e = os.Stat(b.Path); e != nil {
		t.Fatal("invalid approval removed files")
	}
	reaped, e := s.Reap(*p)
	if e != nil {
		t.Fatal(e)
	}
	if reaped.ReapedAt == "" {
		t.Fatal("reap not retained")
	}
	if _, e = os.Lstat(b.Path); !os.IsNotExist(e) {
		t.Fatal("approved checkout remains", e)
	}
	if _, e = s.Reap(*p); e != nil {
		t.Fatal("receipt replay failed", e)
	}
	if _, e = s.Create("task", Options{Repo: b.Repo}, false); !errors.Is(e, ErrExists) {
		t.Fatal("retired key reused", e)
	}
	replacement, e := s.Create("replacement", Options{Repo: b.Repo, Branch: b.Branch}, true)
	if e != nil {
		t.Fatal(e)
	}
	if g(t, replacement.Path, "rev-parse", "HEAD") != b.Checkpoint {
		t.Fatal("reaped checkpoint not recoverable")
	}
}
func TestReapRefusesChangedOrUnconfirmedPlan(t *testing.T) {
	for _, kind := range []string{"artifact", "new-file", "remote"} {
		t.Run(kind, func(t *testing.T) {
			s, b := archivedFixture(t)
			p, e := s.PlanReap("task")
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "artifact":
				write(t, filepath.Join(b.Path, "build", "non-recoverable"), "changed")
			case "new-file":
				write(t, filepath.Join(b.Path, "build", "new"), "keep")
			case "remote":
				g(t, b.Mirror, "push", "origin", ":refs/heads/"+b.Branch)
			}
			if _, e = s.Reap(*p); e == nil {
				t.Fatal("changed/unconfirmed plan accepted")
			}
			if _, e = os.Stat(filepath.Join(b.Path, "file")); e != nil {
				t.Fatal("refusal lost tracked data", e)
			}
		})
	}
}
func TestReapRetainsDurableIntentAndRejectsNewFilesAfterPartialDeletion(t *testing.T) {
	s, b := archivedFixture(t)
	p, e := s.PlanReap("task")
	if e != nil {
		t.Fatal(e)
	}
	s.reapAfterDelete = func() error { return errors.New("simulated crash after content deletion") }
	if _, e = s.Reap(*p); e == nil {
		t.Fatal("injected failure ignored")
	}
	states, e := s.Status("task")
	if e != nil || states[0].ReapingDigest != p.Digest {
		t.Fatal("approval intent not durable", e)
	}
	write(t, filepath.Join(b.Path, "new-work"), "keep")
	s.reapAfterDelete = nil
	if _, e = s.Reap(*p); e == nil {
		t.Fatal("new work deleted on resume")
	}
	if e = os.Remove(filepath.Join(b.Path, "new-work")); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Reap(*p); e != nil {
		t.Fatal("partial deletion did not resume", e)
	}
}
func TestArchiveRequiresReleasedCleanCheckpoint(t *testing.T) {
	s, r := fixture(t)
	b, e := s.Create("task", Options{Repo: r}, false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Archive("task"); e == nil {
		t.Fatal("uncheckpointed archive accepted")
	}
	l, e := s.Lease("task", "writer")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Checkpoint("task", l.Lease.Token); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Archive("task"); !errors.Is(e, ErrBusy) {
		t.Fatal("held archive accepted", e)
	}
	if e = s.Release("task", l.Lease.Token); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(b.Path, "new"), "keep")
	if _, e = s.Archive("task"); !errors.Is(e, ErrDirty) {
		t.Fatal("dirty archive accepted", e)
	}
}

func TestPlanDoesNotCallHiddenOrFilteredLocalBytesRecoverable(t *testing.T) {
	s, b := archivedFixture(t)
	// Git's assume-unchanged bit can make status clean despite local source edits.
	g(t, b.Path, "update-index", "--assume-unchanged", "file")
	write(t, filepath.Join(b.Path, "file"), "uncommitted hidden source")
	p, e := s.PlanReap("task")
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range p.Tree.Entries {
		if v.Path == "file" {
			if v.Recoverable {
				t.Fatal("hidden local edits called recoverable")
			}
			return
		}
	}
	t.Fatal("source omitted from plan")
}

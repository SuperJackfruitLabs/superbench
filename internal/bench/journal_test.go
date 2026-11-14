package bench

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreationJournalReconciles(t *testing.T) {
	s, r := fixture(t)
	b, e := s.Create("task", Options{Repo: r}, false)
	if e != nil {
		t.Fatal(e)
	}
	// The persisted intent plus completed checkout is exactly the state left when
	// creation dies before committing benches.json.
	if e = os.Remove(filepath.Join(s.State, "benches.json")); e != nil {
		t.Fatal(e)
	}
	resumed, e := s.Create("task", Options{Repo: r}, false)
	if e != nil {
		t.Fatal(e)
	}
	if resumed.Path != b.Path {
		t.Fatal("did not adopt original checkout")
	}
	if g(t, resumed.Path, "rev-parse", "HEAD") != g(t, b.Path, "rev-parse", "HEAD") {
		t.Fatal("changed source")
	}
}
func TestCreationJournalPreservesConflict(t *testing.T) {
	for _, mode := range []string{"dirty", "changed-head", "missing-journal", "wrong-base", "wrong-repo"} {
		t.Run(mode, func(t *testing.T) {
			s, r := fixture(t)
			b, e := s.Create("task", Options{Repo: r}, false)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.Remove(filepath.Join(s.State, "benches.json")); e != nil {
				t.Fatal(e)
			}
			options := Options{Repo: r}
			switch mode {
			case "dirty":
				write(t, filepath.Join(b.Path, "untracked"), "preserve")
			case "changed-head":
				commit(t, b)
			case "missing-journal":
				if e = os.Remove(s.journalPath("task")); e != nil {
					t.Fatal(e)
				}
			case "wrong-base":
				options.Base = "other"
			case "wrong-repo":
				options.Repo = r + ".different"
			}
			before := g(t, b.Path, "rev-parse", "HEAD")
			if _, e = s.Create("task", options, false); e == nil {
				t.Fatal("conflicting checkout adopted")
			}
			if after := g(t, b.Path, "rev-parse", "HEAD"); after != before {
				t.Fatal("checkout changed")
			}
			if mode == "dirty" {
				raw, e := os.ReadFile(filepath.Join(b.Path, "untracked"))
				if e != nil || string(raw) != "preserve" {
					t.Fatal("lost untracked data")
				}
			}
		})
	}
}

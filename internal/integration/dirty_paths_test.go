package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/scalarapi"
)

// A dirty refusal names what the holder must clean, and only that: ignored
// files (.gitignore and the repository's info/exclude) are not offending.
func TestDirtyReleaseNamesOffendingPathsOnly(t *testing.T) {
	s, b, _, _ := fixture(t)
	held, e := s.LeaseContext(context.Background(), "task", "stage")
	if e != nil || held.Lease == nil {
		t.Fatal(held, e)
	}
	common := git(t, b.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e = os.MkdirAll(filepath.Join(common, "info"), 0700); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(common, "info", "exclude"), "excluded/\n")
	for _, dir := range []string{"src/__pycache__", ".agent/cache/scratch", "excluded"} {
		if e = os.MkdirAll(filepath.Join(b.Path, dir), 0700); e != nil {
			t.Fatal(e)
		}
	}
	write(t, filepath.Join(b.Path, "file"), "SECRET-CONTENT")
	write(t, filepath.Join(b.Path, "src", "__pycache__", "a.cpython-312.pyc"), "SECRET-CONTENT")
	write(t, filepath.Join(b.Path, ".agent", "cache", "scratch", "run_tests.py"), "SECRET-CONTENT")
	write(t, filepath.Join(b.Path, "ignored"), "warm cache")
	write(t, filepath.Join(b.Path, "excluded", "cache.bin"), "warm cache")
	e = s.Release("task", held.Lease.Token)
	if !errors.Is(e, bench.ErrDirty) {
		t.Fatal("dirty release accepted", e)
	}
	status, code, message := scalarapi.ErrorStatus(e)
	want := "bench has uncommitted or untracked files; commit or remove them before checkpoint or release: file, .agent/cache/scratch/run_tests.py, src/__pycache__/a.cpython-312.pyc"
	if status != 409 || code != "conflict" || message != want {
		t.Fatalf("got %d %s %q\nwant %q", status, code, message, want)
	}
}

package repository

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func mergeFixture(t *testing.T) (Mirrors, string, string, Checkout, string) {
	t.Helper()
	m, remote, seed := fixture(t)
	g(t, seed, "checkout", "-b", "task")
	write(t, filepath.Join(seed, "task-file"), "task work")
	g(t, seed, "add", ".")
	g(t, seed, "commit", "-m", "task work")
	checkpoint := g(t, seed, "rev-parse", "HEAD")
	g(t, seed, "push", "origin", "task")
	g(t, seed, "checkout", "main")
	mirror, err := m.Prepare(context.Background(), remote)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work")
	g(t, mirror, "worktree", "add", work, "task")
	return m, remote, seed, Checkout{Path: work, Branch: "task", Mirror: mirror}, checkpoint
}

func mergeTask(t *testing.T, seed string) string {
	t.Helper()
	g(t, seed, "merge", "--no-ff", "task", "-m", "merge task")
	g(t, seed, "push", "origin", "main")
	return g(t, seed, "rev-parse", "HEAD")
}

func TestMergedUsesActualRemoteDefaultAndRefreshes(t *testing.T) {
	m, remote, seed, c, checkpoint := mergeFixture(t)
	if evidence, err := m.Merged(context.Background(), remote, c, checkpoint); err != nil || evidence != (MergeEvidence{}) {
		t.Fatalf("unmerged branch: %+v, %v", evidence, err)
	}
	want := mergeTask(t, seed)
	if evidence, err := m.Merged(context.Background(), remote, c, checkpoint); err != nil || evidence != (MergeEvidence{Branch: "refs/heads/main", Commit: want}) {
		t.Fatalf("merged branch: %+v, %v", evidence, err)
	}
	// Changing the remote default must override the bare clone's stale HEAD.
	g(t, seed, "branch", "trunk")
	g(t, seed, "push", "origin", "trunk")
	g(t, remote, "symbolic-ref", "HEAD", "refs/heads/trunk")
	if evidence, err := m.Merged(context.Background(), remote, c, checkpoint); err != nil || evidence.Branch != "refs/heads/trunk" || evidence.Commit != want {
		t.Fatalf("changed default: %+v, %v", evidence, err)
	}
	if got := g(t, c.Path, "rev-parse", "HEAD"); got != checkpoint {
		t.Fatal("merge observation changed checkout", got)
	}
}

func TestMergedRequiresExactLiveTaskAndDistinctDefault(t *testing.T) {
	for _, kind := range []string{"deleted", "advanced", "force-pushed", "task-is-default", "squashed"} {
		t.Run(kind, func(t *testing.T) {
			m, remote, seed, c, checkpoint := mergeFixture(t)
			if kind == "squashed" {
				g(t, seed, "merge", "--squash", "task")
				g(t, seed, "commit", "-m", "squashed task")
				g(t, seed, "push", "origin", "main")
			} else {
				mergeTask(t, seed)
			}
			switch kind {
			case "deleted":
				g(t, remote, "update-ref", "-d", "refs/heads/task")
			case "advanced":
				g(t, seed, "checkout", "task")
				write(t, filepath.Join(seed, "extra"), "new task work")
				g(t, seed, "add", ".")
				g(t, seed, "commit", "-m", "advance task")
				g(t, seed, "push", "origin", "task")
			case "force-pushed":
				g(t, remote, "update-ref", "refs/heads/task", g(t, seed, "rev-parse", "main^1"))
			case "task-is-default":
				g(t, remote, "symbolic-ref", "HEAD", "refs/heads/task")
			}
			if evidence, err := m.Merged(context.Background(), remote, c, checkpoint); err != nil || evidence != (MergeEvidence{}) {
				t.Fatalf("%s treated as merge: %+v, %v", kind, evidence, err)
			}
		})
	}
}

func TestMergedRejectsUnavailableOrChangedAuthority(t *testing.T) {
	for _, kind := range []string{"changed-origin", "unborn-default", "detached-default", "invalid-checkpoint", "missing-object", "remote-error"} {
		t.Run(kind, func(t *testing.T) {
			m, remote, seed, c, checkpoint := mergeFixture(t)
			mergeTask(t, seed)
			switch kind {
			case "changed-origin":
				g(t, c.Mirror, "config", "remote.origin.url", remote+"-other")
			case "unborn-default":
				g(t, remote, "symbolic-ref", "HEAD", "refs/heads/missing")
			case "detached-default":
				g(t, remote, "update-ref", "--no-deref", "HEAD", checkpoint)
			case "invalid-checkpoint":
				checkpoint = "main"
			case "missing-object":
				checkpoint = "1111111111111111111111111111111111111111"
			case "remote-error":
				if err := os.Rename(remote, remote+"-gone"); err != nil {
					t.Fatal(err)
				}
			}
			// A valid but different checkpoint is simply not the current task tip.
			if kind == "missing-object" {
				if evidence, err := m.Merged(context.Background(), remote, c, checkpoint); err != nil || evidence != (MergeEvidence{}) {
					t.Fatal(evidence, err)
				}
				return
			}
			if evidence, err := m.Merged(context.Background(), remote, c, checkpoint); err == nil || evidence != (MergeEvidence{}) {
				t.Fatalf("%s accepted: %+v, %v", kind, evidence, err)
			}
		})
	}
}

func TestMergedRejectsFetchAdvertisementRaceAndGitErrors(t *testing.T) {
	for _, kind := range []string{"remote-moved", "merge-base-error", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			m, remote, seed, c, checkpoint := mergeFixture(t)
			mergeTask(t, seed)
			real, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			script := ""
			switch kind {
			case "remote-moved":
				// Change main after the atomic fetch, before its live advertisement.
				script = "if [ \"$fixture_git_verb\" = ls-remote ] && [ \"$fixture_git_first_argument\" = --symref ]; then " + quote(real) + " -C " + quote(remote) + " update-ref refs/heads/main " + quote(checkpoint) + "; fi\n"
			case "merge-base-error":
				script = "if [ \"$fixture_git_verb\" = merge-base ]; then exit 128; fi\n"
			case "timeout":
				m.Timeout = 500 * time.Millisecond
				script = "if [ \"$fixture_git_verb\" = ls-remote ]; then exec sleep 30; fi\n"
			}
			wrapper(t, script+"exec "+quote(real)+" \"$@\"\n")
			evidence, err := m.Merged(context.Background(), remote, c, checkpoint)
			if err == nil || evidence != (MergeEvidence{}) {
				t.Fatal(evidence, err)
			}
			if kind == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		})
	}
}

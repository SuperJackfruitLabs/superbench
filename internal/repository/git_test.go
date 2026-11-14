package repository

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func wrapper(t *testing.T, text string) {
	t.Helper()
	dir := t.TempDir()
	// Inspect the verb without shifting positional arguments: production adds
	// command-local Git policy flags, all of which must reach the real process.
	probe := `fixture_git_verb=
fixture_git_first_argument=
fixture_git_skip=
for fixture_git_word do
 if [ -n "$fixture_git_skip" ]; then fixture_git_skip=; continue; fi
 if [ -n "$fixture_git_verb" ]; then fixture_git_first_argument=$fixture_git_word; break; fi
 case "$fixture_git_word" in
  -C|-c|--git-dir|--work-tree|--namespace|--config-env) fixture_git_skip=yes ;;
  -*) ;;
  *) fixture_git_verb=$fixture_git_word ;;
 esac
done
`
	write(t, filepath.Join(dir, "git"), "#!/bin/sh\n"+probe+text)
	if e := os.Chmod(filepath.Join(dir, "git"), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
func TestRefreshDeadlineAndCancellation(t *testing.T) {
	for _, kind := range []string{"deadline", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			m, remote, _ := fixture(t)
			mirror, e := m.Prepare(context.Background(), remote)
			if e != nil {
				t.Fatal(e)
			}
			real, e := exec.LookPath("git")
			if e != nil {
				t.Fatal(e)
			}
			marker := filepath.Join(t.TempDir(), "fetch-started")
			wrapper(t, "if [ \"$fixture_git_verb\" = fetch ]; then printf started > "+quote(marker)+"; exec sleep 30; fi\nexec "+quote(real)+" \"$@\"\n")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "deadline" {
				m.Timeout = 500 * time.Millisecond
			}
			result := make(chan error, 1)
			start := time.Now()
			go func() { result <- m.Refresh(ctx, remote, mirror) }()
			for {
				if _, e = os.Stat(marker); e == nil {
					break
				}
				select {
				case e = <-result:
					// The same total budget covers validation and fetch. On a busy
					// host it may expire before the stalled fetch is even launched.
					if kind == "deadline" && errors.Is(e, context.DeadlineExceeded) && time.Since(start) >= m.Timeout {
						return
					}
					t.Fatal("refresh ended before fetch", e)
				default:
				}
				if time.Since(start) > 5*time.Second {
					t.Fatal("fetch did not start")
				}
				time.Sleep(time.Millisecond)
			}
			want := context.DeadlineExceeded
			if kind == "cancel" {
				cancel()
				want = context.Canceled
			}
			select {
			case e = <-result:
				if !errors.Is(e, want) {
					t.Fatal(e, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("mirror command did not stop")
			}
		})
	}
}
func TestRunnerBoundsOutputAndDoesNotEchoDiagnostics(t *testing.T) {
	t.Run("output-bound", func(t *testing.T) {
		wrapper(t, "head -c 131072 /dev/zero\n")
		if _, e := git(context.Background(), t.TempDir(), "rev-parse"); e == nil || !strings.Contains(e.Error(), "64 KiB") {
			t.Fatal(e)
		}
	})
	t.Run("diagnostics", func(t *testing.T) {
		wrapper(t, "printf private-diagnostic >&2; head -c 131072 /dev/zero >&2; exit 1\n")
		if _, e := git(context.Background(), t.TempDir(), "fetch"); e == nil || strings.Contains(e.Error(), "private-diagnostic") {
			t.Fatal(e)
		}
	})
}
func TestMirrorCommandsIgnoreAmbientRepositoryAndCommandConfig(t *testing.T) {
	m, remote, _ := fixture(t)
	mirror, e := m.Prepare(context.Background(), remote)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "not-the-mirror"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "remote.origin.url")
	t.Setenv("GIT_CONFIG_VALUE_0", "other-origin")
	t.Setenv("BENCH_LEASE_TOKEN", "fixture-capability")
	real, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	wrapper(t, "test -z \"${BENCH_LEASE_TOKEN-}\" || exit 9\nexec "+quote(real)+" \"$@\"\n")
	if e = m.Refresh(context.Background(), remote, mirror); e != nil {
		t.Fatal(e)
	}
}

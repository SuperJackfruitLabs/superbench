package integration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatal(string(out), e)
	}
	return strings.TrimSpace(string(out))
}
func write(t *testing.T, path, text string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T) (bench.Store, *bench.Bench, string, string) {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return os.Chmod(path, 0700)
			}
			return nil
		})
	})
	remote, seed := filepath.Join(root, "remote.git"), filepath.Join(root, "seed")
	git(t, root, "init", "--bare", remote)
	git(t, root, "init", "-b", "main", seed)
	git(t, seed, "config", "user.name", "Test")
	git(t, seed, "config", "user.email", "test@example.invalid")
	write(t, filepath.Join(seed, "file"), "base")
	write(t, filepath.Join(seed, ".gitignore"), "ignored\n")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "base")
	git(t, seed, "remote", "add", "origin", remote)
	git(t, seed, "push", "origin", "main")
	git(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	s := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	b, e := s.Create("task", bench.Options{Repo: remote}, false)
	if e != nil {
		t.Fatal(e)
	}
	return s, b, remote, seed
}

func TestLeaseRefreshesRemoteTrackingWithoutChangingWork(t *testing.T) {
	s, b, _, seed := fixture(t)
	head := git(t, b.Path, "rev-parse", "HEAD")
	write(t, filepath.Join(b.Path, "file"), "dirty")
	write(t, filepath.Join(b.Path, "untracked"), "preserved")
	write(t, filepath.Join(b.Path, "ignored"), "warm cache")
	status := git(t, b.Path, "status", "--porcelain")
	write(t, filepath.Join(seed, "file"), "remote update")
	git(t, seed, "add", "file")
	git(t, seed, "commit", "-m", "remote update")
	want := git(t, seed, "rev-parse", "HEAD")
	git(t, seed, "push", "origin", "main")
	held, e := s.LeaseContext(context.Background(), "task", "next-stage")
	if e != nil || held.Lease == nil {
		t.Fatal(held, e)
	}
	if git(t, b.Mirror, "rev-parse", "refs/remotes/origin/main") != want {
		t.Fatal("acquisition retained a stale mirror")
	}
	if git(t, b.Path, "rev-parse", "HEAD") != head || git(t, b.Path, "status", "--porcelain") != status {
		t.Fatal("acquisition rewrote work")
	}
	for name, want := range map[string]string{"file": "dirty", "untracked": "preserved", "ignored": "warm cache"} {
		raw, e := os.ReadFile(filepath.Join(b.Path, name))
		if e != nil || string(raw) != want {
			t.Fatal(name, e)
		}
	}
	if e = s.Release("task", held.Lease.Token); !errors.Is(e, bench.ErrDirty) {
		t.Fatal("dirty work lost release protection", e)
	}
}
func TestFailedOrCanceledRefreshGrantsNoLeaseAndRepairStaysAvailable(t *testing.T) {
	s, b, remote, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.LeaseContext(ctx, "task", "canceled"); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e := os.Rename(remote, remote+".offline"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.LeaseContext(context.Background(), "task", "ordinary"); e == nil {
		t.Fatal("offline remote granted ordinary acquisition")
	}
	status, e := s.Status("task")
	if e != nil || status[0].Lease != nil {
		t.Fatal("failed fetch persisted ownership", e)
	}
	if held, e := s.RepairLease("task", "operator"); e != nil || held.Lease == nil {
		t.Fatal("repair depends on remote availability", held, e)
	}
	if raw, e := os.ReadFile(filepath.Join(b.Path, "file")); e != nil || string(raw) != "base" {
		t.Fatal("failure destroyed checkout", e)
	}
}
func TestAcquisitionDoesNotRecreateMissingRegisteredMirror(t *testing.T) {
	s, b, _, _ := fixture(t)
	if e := os.Rename(b.Mirror, b.Mirror+".retained"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.LeaseContext(context.Background(), "task", "ordinary"); e == nil {
		t.Fatal("missing mirror was accepted")
	}
	if _, e := os.Stat(b.Mirror); !os.IsNotExist(e) {
		t.Fatal("acquisition replaced the missing registered mirror")
	}
	if raw, e := os.ReadFile(filepath.Join(b.Path, "file")); e != nil || string(raw) != "base" {
		t.Fatal("checkout was lost", e)
	}
	status, e := s.Status("task")
	if e != nil || status[0].Lease != nil {
		t.Fatal("missing mirror granted a lease", e)
	}
}

func TestAcquisitionRefusesChangedCheckoutIdentityWithoutReset(t *testing.T) {
	for _, kind := range []string{"branch", "detached", "linked", "other-repository"} {
		t.Run(kind, func(t *testing.T) {
			s, b, _, _ := fixture(t)
			switch kind {
			case "branch":
				git(t, b.Path, "checkout", "-b", "other")
			case "detached":
				git(t, b.Path, "checkout", "--detach")
			case "linked":
				if e := os.Rename(b.Path, b.Path+".saved"); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(b.Path+".saved", b.Path); e != nil {
					t.Fatal(e)
				}
			case "other-repository":
				if e := os.Rename(b.Path, b.Path+".saved"); e != nil {
					t.Fatal(e)
				}
				git(t, filepath.Dir(b.Path), "init", "-b", b.Branch, b.Path)
				write(t, filepath.Join(b.Path, "file"), "replacement")
			}
			if _, e := s.LeaseContext(context.Background(), "task", "ordinary"); e == nil {
				t.Fatal("changed checkout accepted")
			}
			status, e := s.Status("task")
			if e != nil || status[0].Lease != nil {
				t.Fatal("changed checkout acquired", e)
			}
			want := "base"
			if kind == "other-repository" {
				want = "replacement"
			}
			if raw, e := os.ReadFile(filepath.Join(b.Path, "file")); e != nil || string(raw) != want {
				t.Fatal("acquisition reset changed work", e)
			}
		})
	}
}

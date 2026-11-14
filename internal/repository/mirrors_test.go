package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func g(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = environment(os.Environ())
	out, e := c.CombinedOutput()
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
func fixture(t *testing.T) (Mirrors, string, string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if e := os.Mkdir(state, 0700); e != nil {
		t.Fatal(e)
	}
	remote, seed := filepath.Join(root, "remote.git"), filepath.Join(root, "seed")
	g(t, root, "init", "--bare", remote)
	g(t, root, "init", "-b", "main", seed)
	g(t, seed, "config", "user.name", "Test")
	g(t, seed, "config", "user.email", "test@example.invalid")
	write(t, filepath.Join(seed, "file"), "base")
	write(t, filepath.Join(seed, ".gitignore"), "ignored\n")
	g(t, seed, "add", ".")
	g(t, seed, "commit", "-m", "base")
	g(t, seed, "remote", "add", "origin", remote)
	g(t, seed, "push", "origin", "main")
	g(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	return Mirrors{State: state, Data: filepath.Join(root, "data")}, remote, seed
}
func TestRefreshPreservesLocalWorkAndIgnoresConfiguredRefmaps(t *testing.T) {
	m, remote, seed := fixture(t)
	mirror, e := m.Prepare(context.Background(), remote)
	if e != nil {
		t.Fatal(e)
	}
	base := g(t, mirror, "rev-parse", "refs/remotes/origin/main")
	work := filepath.Join(t.TempDir(), "work")
	g(t, mirror, "worktree", "add", "-b", "task", work, "refs/remotes/origin/main")
	write(t, filepath.Join(work, "file"), "local commit")
	g(t, work, "add", "file")
	g(t, work, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "unpublished")
	local := g(t, work, "rev-parse", "HEAD")
	g(t, mirror, "tag", "local-tag", local)
	write(t, filepath.Join(work, "file"), "dirty")
	write(t, filepath.Join(work, "untracked"), "untracked")
	write(t, filepath.Join(work, "ignored"), "cache")
	status := g(t, work, "status", "--porcelain")
	write(t, filepath.Join(seed, "file"), "remote update")
	g(t, seed, "add", "file")
	g(t, seed, "commit", "-m", "remote update")
	want := g(t, seed, "rev-parse", "HEAD")
	g(t, seed, "push", "origin", "main")
	g(t, remote, "update-ref", "refs/heads/task", want)
	g(t, remote, "update-ref", "refs/tags/local-tag", want)
	g(t, mirror, "config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/heads/*")
	g(t, mirror, "config", "fetch.pruneTags", "true")
	g(t, mirror, "config", "remote.origin.pruneTags", "true")
	if e = m.Refresh(context.Background(), remote, mirror); e != nil {
		t.Fatal(e)
	}
	for ref, value := range map[string]string{"refs/remotes/origin/main": want, "refs/remotes/origin/task": want, "refs/heads/main": base, "refs/heads/task": local, "refs/tags/local-tag": local} {
		if got := g(t, mirror, "rev-parse", ref); got != value {
			t.Fatal("wrong refreshed namespace", ref, got, value)
		}
	}
	if g(t, work, "rev-parse", "HEAD") != local || g(t, work, "status", "--porcelain") != status {
		t.Fatal("refresh changed local work")
	}
	for name, want := range map[string]string{"file": "dirty", "untracked": "untracked", "ignored": "cache"} {
		raw, e := os.ReadFile(filepath.Join(work, name))
		if e != nil || string(raw) != want {
			t.Fatal(name, e)
		}
	}
	g(t, remote, "update-ref", "-d", "refs/heads/task")
	if e = m.Refresh(context.Background(), remote, mirror); e != nil {
		t.Fatal(e)
	}
	c := exec.Command("git", "-C", mirror, "show-ref", "--verify", "refs/remotes/origin/task")
	if c.Run() == nil || g(t, mirror, "rev-parse", "refs/heads/task") != local {
		t.Fatal("pruning touched local task or retained deleted tracking ref")
	}
}
func TestRefreshRefusesMissingLinkedChangedOrAmbiguousOrigin(t *testing.T) {
	for _, kind := range []string{"missing", "linked", "linked-parent", "changed-origin", "multiple-origins", "wrong-path"} {
		t.Run(kind, func(t *testing.T) {
			m, remote, _ := fixture(t)
			mirror, e := m.Prepare(context.Background(), remote)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "missing", "linked":
				if e = os.Rename(mirror, mirror+".saved"); e == nil && kind == "linked" {
					e = os.Symlink(mirror+".saved", mirror)
				}
			case "linked-parent":
				parent := filepath.Dir(mirror)
				if e = os.Rename(parent, parent+".saved"); e == nil {
					e = os.Symlink(parent+".saved", parent)
				}
			case "changed-origin":
				g(t, mirror, "config", "remote.origin.url", remote+"-other")
			case "multiple-origins":
				g(t, mirror, "config", "--add", "remote.origin.url", remote)
			case "wrong-path":
				mirror = mirror + ".other"
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = m.Refresh(context.Background(), remote, mirror); e == nil {
				t.Fatal("unsafe mirror accepted")
			}
			if kind == "missing" {
				if _, e = os.Stat(mirror); !os.IsNotExist(e) {
					t.Fatal("acquisition recreated a missing mirror")
				}
			}
		})
	}
}

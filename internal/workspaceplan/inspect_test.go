package workspaceplan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func inspectGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := inspectRunner(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err, out)
	}
	return strings.TrimSpace(out)
}
func inspectRunner(ctx context.Context, dir string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := c.CombinedOutput()
	return string(out), err
}
func inspectFixture(t *testing.T) Plan {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(data, "mirrors"), 0700); err != nil {
		t.Fatal(err)
	}
	p := Plan{Key: "card", Root: filepath.Join(data, "benches", "card"), CheckpointRepository: "a"}
	for _, name := range []string{"a", "b"} {
		repo := filepath.Join(root, name)
		if err := os.Mkdir(repo, 0700); err != nil {
			t.Fatal(err)
		}
		inspectGit(t, repo, "init", "-b", "main")
		if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("original\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ignored\n"), 0600); err != nil {
			t.Fatal(err)
		}
		inspectGit(t, repo, "add", ".")
		inspectGit(t, repo, "commit", "-m", "initial")
		// Use the same repository identity/layout as durable plans.
		m := Member{Name: name, Repository: repo, Branch: "bench/card", Base: "main", Path: filepath.Join(p.Root, name), ObjectFormat: "sha1"}
		m.Mirror = repository.Path(data, repo)
		inspectGit(t, root, "clone", "--mirror", repo, m.Mirror)
		m.Source = inspectGit(t, repo, "rev-parse", "HEAD")
		p.Members = append(p.Members, m)
	}
	return p
}
func TestInspectMaterializationPreservesDirtyCompleteMemberAndAbsentSecond(t *testing.T) {
	p := inspectFixture(t)
	a := p.Members[0]
	inspectGit(t, a.Mirror, "worktree", "add", "-b", a.Branch, a.Path, a.Source)
	for _, file := range []string{"tracked", "ignored", "untracked"} {
		if err := os.WriteFile(filepath.Join(a.Path, file), []byte("keep "+file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	inspectGit(t, a.Path, "add", "tracked")
	index := inspectGit(t, a.Path, "rev-parse", "--path-format=absolute", "--git-path", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	i := Inspector{Git: inspectRunner}
	o, err := i.Inspect(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if o.Members[0].Disposition != "checkout" || o.Members[1].Disposition != "absent" || o.Members[0].Head != a.Source || o.Members[0].StatusSHA256 == "" {
		t.Fatal(o)
	}
	if err := i.Verify(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(index)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("inspection rewrote index", err)
	}
	if _, err := os.Lstat(p.Members[1].Path); !os.IsNotExist(err) {
		t.Fatal("inspection materialized missing member", err)
	}
	for _, file := range []string{"tracked", "ignored", "untracked"} {
		raw, err := os.ReadFile(filepath.Join(a.Path, file))
		if err != nil || string(raw) != "keep "+file {
			t.Fatal("inspection changed bytes", file, err)
		}
	}
	if err := os.WriteFile(filepath.Join(a.Path, "new"), []byte("late"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := i.Verify(context.Background(), o); err == nil {
		t.Fatal("late untracked entry accepted")
	}
}
func TestInspectMaterializationWholePlanConflictsBeforeEffects(t *testing.T) {
	for _, kind := range []string{"occupied", "stale", "unknown", "symlink", "empty", "cancel", "promisor"} {
		t.Run(kind, func(t *testing.T) {
			p := inspectFixture(t)
			b := p.Members[1]
			ctx := context.Background()
			switch kind {
			case "promisor":
				inspectGit(t, b.Mirror, "config", "remote.origin.promisor", "true")
			case "occupied", "stale":
				elsewhere := filepath.Join(filepath.Dir(p.Root), "other")
				inspectGit(t, b.Mirror, "worktree", "add", "-b", b.Branch, elsewhere, b.Source)
				if kind == "stale" {
					if err := os.RemoveAll(elsewhere); err != nil {
						t.Fatal(err)
					}
				}
			case "unknown":
				if err := os.MkdirAll(b.Path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(b.Path, "keep"), []byte("unknown"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.MkdirAll(p.Root, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(b.Mirror, b.Path); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.MkdirAll(b.Path, 0700); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			o, err := (Inspector{Git: inspectRunner}).Inspect(ctx, p)
			if kind == "empty" {
				if err != nil || o.Members[1].Disposition != "empty" {
					t.Fatal(o, err)
				}
			} else if err == nil {
				t.Fatal("unsafe whole plan accepted")
			}
			if _, err := os.Lstat(p.Members[0].Path); !os.IsNotExist(err) {
				t.Fatal("first member created", err)
			}
		})
	}
}

func TestInspectAdvancedCheckoutIsPreservedButLateOwnershipChangesRefuse(t *testing.T) {
	p := inspectFixture(t)
	a := p.Members[0]
	inspectGit(t, a.Mirror, "worktree", "add", "-b", a.Branch, a.Path, a.Source)
	inspectGit(t, a.Path, "commit", "--allow-empty", "-m", "operator preserved commit")
	head := inspectGit(t, a.Path, "rev-parse", "HEAD")
	i := Inspector{Git: inspectRunner}
	o, err := i.Inspect(context.Background(), p)
	if err != nil || o.Members[0].Head != head || head == a.Source {
		t.Fatal("advanced registered work not preserved", o, err)
	}
	if err := i.Verify(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	b := p.Members[1]
	elsewhere := filepath.Join(filepath.Dir(p.Root), "late-other")
	inspectGit(t, b.Mirror, "worktree", "add", "-b", b.Branch, elsewhere, b.Source)
	if err := i.Verify(context.Background(), o); err == nil {
		t.Fatal("late branch ownership accepted")
	}
	if got := inspectGit(t, a.Path, "rev-parse", "HEAD"); got != head {
		t.Fatal("preserved branch reset")
	}
	if _, err := os.Lstat(b.Path); !os.IsNotExist(err) {
		t.Fatal("missing target materialized despite conflict", err)
	}
}

func TestInspectDisablesRepositoryFsmonitorAndBindsIndexAndNamedRoot(t *testing.T) {
	p := inspectFixture(t)
	a := p.Members[0]
	inspectGit(t, a.Mirror, "worktree", "add", "-b", a.Branch, a.Path, a.Source)
	marker := filepath.Join(filepath.Dir(p.Root), "fsmonitor-ran")
	script := filepath.Join(filepath.Dir(p.Root), "fsmonitor-hook")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf unsafe > '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	inspectGit(t, a.Path, "config", "core.fsmonitor", script)
	i := Inspector{Git: inspectRunner}
	o, err := i.Inspect(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("inspection executed configured fsmonitor", err)
	}
	// A newly staged blob changes index authority even if both states report M.
	if err := os.WriteFile(filepath.Join(a.Path, "tracked"), []byte("staged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	inspectGit(t, a.Path, "-c", "core.fsmonitor=false", "add", "tracked")
	if err := i.Verify(context.Background(), o); err == nil {
		t.Fatal("changed index accepted")
	}
	o, err = i.Inspect(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p.Root, p.Root+"-preserved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := i.Verify(context.Background(), o); err == nil {
		t.Fatal("replacement named root accepted")
	}
}

package worktreetransfer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fixture struct {
	m                                   Manager
	root, repo, checkout, other, commit string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{root: root}
	f.repo = filepath.Join(f.root, "repo")
	f.checkout = filepath.Join(f.root, "source")
	f.other = filepath.Join(f.root, "unrelated")
	f.m.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		out, e := cmd.CombinedOutput()
		if e != nil {
			return "", errors.New(string(out))
		}
		return strings.TrimSpace(string(out)), nil
	}
	f.m.Gate = func(context.Context, Plan) error { return nil }
	f.git(t, f.root, "init", "--bare", f.repo)
	seed := filepath.Join(f.root, "seed")
	f.git(t, f.root, "init", seed)
	f.git(t, seed, "config", "user.name", "Fixture")
	f.git(t, seed, "config", "user.email", "fixture@example.invalid")
	write(t, filepath.Join(seed, "tracked"), "original\n")
	write(t, filepath.Join(seed, ".gitignore"), "ignored\n")
	f.git(t, seed, "add", ".")
	f.git(t, seed, "commit", "-m", "source")
	f.commit = f.git(t, seed, "rev-parse", "HEAD")
	f.git(t, seed, "push", f.repo, "HEAD:refs/heads/task")
	f.git(t, f.repo, "worktree", "add", f.checkout, "task")
	f.git(t, f.repo, "worktree", "add", "-b", "unrelated", f.other, f.commit)
	return f
}
func (f *fixture) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	v, e := f.m.Git(context.Background(), dir, args...)
	if e != nil {
		t.Fatal(args, e)
	}
	return v
}
func write(t *testing.T, p, s string) {
	t.Helper()
	if e := os.WriteFile(p, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
}
func (f *fixture) plan(t *testing.T) Plan {
	t.Helper()
	p, e := f.m.Inspect(context.Background(), f.checkout, f.repo, "refs/heads/task", f.commit)
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestDetachPreservesDirtyIndexFilesAndOtherRegistration(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.checkout, "tracked"), "staged\n")
	f.git(t, f.checkout, "add", "tracked")
	write(t, filepath.Join(f.checkout, "tracked"), "unstaged\n")
	write(t, filepath.Join(f.checkout, "untracked"), "private\n")
	write(t, filepath.Join(f.checkout, "ignored"), "ignored bytes\n")
	p := f.plan(t)
	other := f.git(t, f.other, "status", "--porcelain")
	calls := 0
	f.m.Gate = func(context.Context, Plan) error { calls++; return nil }
	if e := f.m.Apply(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	if calls != 3 {
		t.Fatal("gate calls", calls)
	}
	detached, e := f.m.InspectDetached(context.Background(), f.checkout, f.repo, p.TaskRef, p.Commit)
	if e != nil || !reflect.DeepEqual(detached, p) {
		t.Fatal("snapshot not preserved", e)
	}
	if e = f.m.Apply(context.Background(), p); e != nil {
		t.Fatal("retry", e)
	}
	if f.git(t, f.repo, "rev-parse", p.TaskRef) != p.Commit || f.git(t, f.other, "status", "--porcelain") != other {
		t.Fatal("unrelated work changed")
	}
	// A subsequent owner can advance the branch without invalidating detached
	// source inspection. Applying the old transfer again must still refuse.
	next := f.git(t, f.repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", f.git(t, f.repo, "rev-parse", p.Commit+"^{tree}"), "-p", p.Commit, "-m", "next")
	f.git(t, f.repo, "update-ref", p.TaskRef, next, p.Commit)
	if _, e = f.m.InspectDetached(context.Background(), f.checkout, f.repo, p.TaskRef, p.Commit); e != nil {
		t.Fatal("advanced destination ref", e)
	}
	if e = f.m.Apply(context.Background(), p); e == nil {
		t.Fatal("old apply accepted advanced ref")
	}
}

func TestFaultAfterHEADUpdatePreservesExactRetry(t *testing.T) {
	for _, where := range []string{"before", "after", "gate"} {
		t.Run(where, func(t *testing.T) {
			f := newFixture(t)
			p := f.plan(t)
			base := f.m.Git
			fault := errors.New("injected fault")
			f.m.Git = func(c context.Context, d string, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "update-ref" {
					if where == "before" {
						return "", fault
					}
					v, e := base(c, d, args...)
					if where == "after" && e == nil {
						return "", fault
					}
					return v, e
				}
				return base(c, d, args...)
			}
			calls := 0
			f.m.Gate = func(context.Context, Plan) error {
				calls++
				if where == "gate" && calls == 3 {
					return fault
				}
				return nil
			}
			if e := f.m.Apply(context.Background(), p); !errors.Is(e, fault) {
				t.Fatal(e)
			}
			f.m.Git = base
			f.m.Gate = func(context.Context, Plan) error { return nil }
			if e := f.m.Apply(context.Background(), p); e != nil {
				t.Fatal("exact retry", e)
			}
		})
	}
}

func TestRejectChangedOrLockedOwnershipWithoutEffects(t *testing.T) {
	for _, kind := range []string{"locked", "dirty", "index", "gitfile", "unknown-head", "missing-registration", "replaced", "nil-gate"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			p := f.plan(t)
			switch kind {
			case "locked":
				f.git(t, f.repo, "worktree", "lock", f.checkout)
			case "dirty":
				write(t, filepath.Join(f.checkout, "tracked"), "changed\n")
			case "index":
				f.git(t, f.checkout, "update-index", "--assume-unchanged", "tracked")
			case "gitfile":
				original := filepath.Join(f.checkout, ".git")
				raw, e := os.ReadFile(original)
				if e != nil {
					t.Fatal(e)
				}
				saved := filepath.Join(f.root, "gitfile")
				write(t, saved, string(raw))
				if e = os.Remove(original); e != nil {
					t.Fatal(e)
				}
				if e = os.Symlink(saved, original); e != nil {
					t.Fatal(e)
				}
			case "unknown-head":
				write(t, filepath.Join(p.Admin, "HEAD"), "ref: refs/heads/unrelated\n")
			case "missing-registration":
				if e := os.Rename(p.Admin, p.Admin+"-moved"); e != nil {
					t.Fatal(e)
				}
			case "replaced":
				path := filepath.Join(f.checkout, "tracked")
				if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
				write(t, path, "original\n")
			case "nil-gate":
				f.m.Gate = nil
			}
			raw, e := os.ReadFile(filepath.Join(p.Admin, "HEAD"))
			if kind == "missing-registration" {
				raw = nil
			} else if e != nil {
				t.Fatal(e)
			}
			if e = f.m.Apply(context.Background(), p); e == nil {
				t.Fatal("accepted changed ownership")
			}
			if kind != "missing-registration" {
				after, e := os.ReadFile(filepath.Join(p.Admin, "HEAD"))
				if e != nil || string(after) != string(raw) {
					t.Fatal("HEAD changed on refusal", e)
				}
			}
		})
	}
}

func TestInspectBoundsAndCancellation(t *testing.T) {
	f := newFixture(t)
	file, e := os.Create(filepath.Join(f.checkout, "large"))
	if e != nil {
		t.Fatal(e)
	}
	if e = file.Truncate(maxBytes + 1); e != nil {
		t.Fatal(e)
	}
	file.Close()
	if _, e = f.m.Inspect(context.Background(), f.checkout, f.repo, "refs/heads/task", f.commit); e == nil {
		t.Fatal("oversized snapshot accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = f.m.Inspect(ctx, f.checkout, f.repo, "refs/heads/task", f.commit); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestWarmIgnoredArtifactsUseCompactFullSnapshot(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.checkout, "ignored")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const size = 65 << 20
	if err = file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteAt([]byte("warm private output"), size-32); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	p := f.plan(t)
	if p.TreeBytes < size || p.TreeEntries < 3 || p.GitFile.Path != ".git" {
		t.Fatal("warm artifact not bound", p.TreeBytes)
	}
	if raw, _ := json.Marshal(p); len(raw) > 16384 {
		t.Fatal("per-file snapshot leaked into approval", len(raw))
	}
	if err = f.m.Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	after, err := f.m.InspectDetached(context.Background(), f.checkout, f.repo, p.TaskRef, p.Commit)
	if err != nil || !reflect.DeepEqual(after, p) {
		t.Fatal("warm artifact changed", err)
	}
	file, err = os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteAt([]byte("changed warm output"), size-32); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if err = f.m.Apply(context.Background(), p); err == nil {
		t.Fatal("changed large ignored bytes accepted")
	}
}

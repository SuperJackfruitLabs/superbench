package worktreetransfer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quarantineWithoutRoot(t *testing.T) (*missingFixture, string) {
	t.Helper()
	f := newMissingFixture(t)
	// Only this disposable, explicitly owned empty source allocation is removed.
	// The registered mirror lives beside it and must survive independently.
	sourceRoot := filepath.Join(f.root, "benches", "source")
	if err := os.MkdirAll(sourceRoot, 0700); err != nil {
		t.Fatal(err)
	}
	f.checkout = filepath.Join(sourceRoot, "member")
	write(t, filepath.Join(f.plan.Admin, "gitdir"), f.checkout+"/.git\n")
	spec := f.plan.Spec
	spec.Checkout = f.checkout
	p, err := f.m.Inspect(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	f.plan = p
	if p.Parent != sourceRoot {
		t.Fatal("fixture did not bind owned source root")
	}
	if err = f.m.Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(sourceRoot); err != nil {
		t.Fatal(err)
	}
	return f, sourceRoot
}

func TestVerifyQuarantineSurvivesApprovedRootRemovalAndTaskAdvance(t *testing.T) {
	f, sourceRoot := quarantineWithoutRoot(t)
	p := f.plan
	if _, err := os.Lstat(sourceRoot); !os.IsNotExist(err) {
		t.Fatal("source root was not removed", err)
	}
	if err := f.m.VerifyRetained(context.Background(), p); err == nil {
		t.Fatal("ordinary retained proof ignored removed parent")
	}
	if err := f.m.Preflight(context.Background(), p); err == nil {
		t.Fatal("ordinary preflight ignored removed parent")
	}
	// A fresh destination owns the original task branch and advances it normally.
	destination := filepath.Join(f.root, "destination")
	f.git(t, f.repo, "worktree", "add", destination, "task")
	f.git(t, f.repo, "update-ref", p.Spec.TaskRef, f.reflog, p.Spec.Commit)
	e, err := f.m.loadEvidence(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	before, err := adminSnapshot(context.Background(), p.Admin)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := os.ReadFile(p.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	originalGit := f.m.Git
	calls := 0
	f.m.Gate = func(context.Context, MissingPlan) error {
		t.Fatal("quarantine observation requested mutation authority")
		return nil
	}
	f.m.VerifyStorage = nil // The old allocation is deliberately no longer authority.
	f.m.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded read-only Git")
		}
		allowed := false
		for _, arg := range args {
			if arg == "config" || arg == "for-each-ref" || arg == "cat-file" {
				allowed = true
			}
		}
		if !allowed || !strings.Contains(strings.Join(args, " "), "core.hooksPath=/dev/null") {
			t.Fatal("unexpected unmanaged or mutating Git command", args)
		}
		calls++
		return originalGit(ctx, dir, args...)
	}
	if err = f.m.VerifyQuarantine(context.Background(), p); err != nil {
		t.Fatal("quarantine depended on reaped source root", err)
	}
	if calls == 0 {
		t.Fatal("objects were not independently read")
	}
	after, err := adminSnapshot(context.Background(), p.Admin)
	if err != nil {
		t.Fatal(err)
	}
	if !entriesEqual(before, after) {
		t.Fatal("read-only quarantine verifier changed admin")
	}
	actual, err := os.ReadFile(p.Evidence)
	if err != nil || !bytes.Equal(actual, evidence) {
		t.Fatal("read-only verifier changed evidence", err)
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(p.Evidence), e.Stage, "admin", "index")); err != nil {
		t.Fatal("retained index lost", err)
	}
	if raw, err := os.ReadFile(filepath.Join(f.saved, "tracked")); err != nil || string(raw) != "unpublished staged bytes\n" {
		t.Fatal("preserved unpublished source changed", err)
	}
	// Evidence-only verification must not inspect or claim absence of a future
	// unrelated checkout at the old path. The archive caller owns that observation.
	if err := os.MkdirAll(p.Spec.Checkout, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(p.Spec.Checkout, "sentinel"), "do not touch new files")
	if err = f.m.VerifyQuarantine(context.Background(), p); err != nil {
		t.Fatal("evidence-only API consulted old checkout", err)
	}
	if raw, err := os.ReadFile(filepath.Join(p.Spec.Checkout, "sentinel")); err != nil || string(raw) != "do not touch new files" {
		t.Fatal("new checkout touched", err)
	}
}

func entriesEqual(a, b []Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestVerifyQuarantineRefusesChangedRetainedEvidence(t *testing.T) {
	for _, fault := range []string{"admin", "published-evidence", "owned-lock", "copied-index", "lost-pin", "lost-object", "extra-pin", "symbolic-head", "config", "duplicate-registration"} {
		t.Run(fault, func(t *testing.T) {
			f, _ := quarantineWithoutRoot(t)
			p := f.plan
			switch fault {
			case "admin":
				if err := os.Rename(p.Admin, filepath.Join(f.root, "original-admin")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p.Admin, 0700); err != nil {
					t.Fatal(err)
				}
			case "published-evidence":
				raw, err := os.ReadFile(p.Evidence)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(p.Evidence, filepath.Join(f.root, "original-publication")); err != nil {
					t.Fatal(err)
				}
				write(t, p.Evidence, string(raw))
			case "owned-lock":
				path := filepath.Join(p.Admin, "locked")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(path, filepath.Join(f.root, "original-lock")); err != nil {
					t.Fatal(err)
				}
				write(t, path, string(raw))
			case "copied-index":
				e, err := f.m.loadEvidence(context.Background(), p)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(filepath.Dir(p.Evidence), e.Stage, "admin", "index")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(path, filepath.Join(f.root, "original-copy-index")); err != nil {
					t.Fatal(err)
				}
				write(t, path, string(raw))
			case "lost-pin":
				f.git(t, f.repo, "update-ref", "-d", f.m.refPrefix(p)+f.staged)
			case "lost-object":
				path := filepath.Join(f.repo, "objects", f.staged[:2], f.staged[2:])
				if err := os.Rename(path, filepath.Join(f.root, "original-staged-blob")); err != nil {
					t.Fatal(err)
				}
			case "extra-pin":
				f.git(t, f.repo, "update-ref", f.m.refPrefix(p)+"unknown", f.commit)
			case "symbolic-head":
				write(t, filepath.Join(p.Admin, "HEAD"), "ref: "+p.Spec.TaskRef+"\n")
			case "config":
				raw, err := os.ReadFile(filepath.Join(f.repo, "config"))
				if err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(f.repo, "config"), string(raw)+"\n[fixture]\n changed = true\n")
			case "duplicate-registration":
				duplicate := filepath.Join(f.repo, "worktrees", "duplicate")
				if err := os.Mkdir(duplicate, 0700); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(duplicate, "HEAD"), p.Spec.Commit+"\n")
				write(t, filepath.Join(duplicate, "gitdir"), p.Spec.Checkout+"/.git\n")
			}
			head, _ := os.ReadFile(filepath.Join(p.Admin, "HEAD"))
			if err := f.m.VerifyQuarantine(context.Background(), p); err == nil {
				t.Fatal("changed retained evidence was accepted")
			}
			after, _ := os.ReadFile(filepath.Join(p.Admin, "HEAD"))
			if !bytes.Equal(head, after) {
				t.Fatal("failed observation changed retained HEAD")
			}
		})
	}
}

func TestVerifyQuarantineRejectsLateReplacementAndCancellation(t *testing.T) {
	f, _ := quarantineWithoutRoot(t)
	p := f.plan
	original := f.m.Git
	replaced := false
	f.m.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		raw, err := original(ctx, dir, args...)
		if !replaced {
			for _, arg := range args {
				if arg == "cat-file" {
					replaced = true
					path := filepath.Join(p.Admin, "locked")
					before, e := os.ReadFile(path)
					if e != nil {
						t.Fatal(e)
					}
					if e = os.Rename(path, filepath.Join(f.root, "late-original-lock")); e != nil {
						t.Fatal(e)
					}
					write(t, path, string(before))
					break
				}
			}
		}
		return raw, err
	}
	if err := f.m.VerifyQuarantine(context.Background(), p); err == nil || !replaced {
		t.Fatal("late lock replacement accepted", err)
	}
	calls := 0
	f.m.Git = func(context.Context, string, ...string) (string, error) { calls++; return "", nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.m.VerifyQuarantine(ctx, p); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("canceled verifier called Git", err, calls)
	}
	f.m.Timeout = -time.Second
	if err := f.m.VerifyQuarantine(context.Background(), p); err == nil || calls != 0 {
		t.Fatal("invalid timeout called Git", err, calls)
	}
}

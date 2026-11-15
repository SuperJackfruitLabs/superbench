package worktreetransfer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type missingFixture struct {
	*fixture
	m                     MissingManager
	plan                  MissingPlan
	saved, staged, reflog string
	gateCalls             int
}

func newMissingFixture(t *testing.T) *missingFixture {
	t.Helper()
	f := newFixture(t)
	m := &missingFixture{fixture: f, saved: filepath.Join(f.root, "preserved-source")}
	// Retain an index-only blob and a commit that would otherwise survive only
	// through the source HEAD reflog. The confirmed branch remains unchanged.
	write(t, filepath.Join(f.checkout, "tracked"), "unpublished staged bytes\n")
	f.git(t, f.checkout, "add", "tracked")
	m.staged = f.git(t, f.checkout, "rev-parse", ":tracked")
	tree := f.git(t, f.repo, "rev-parse", f.commit+"^{tree}")
	m.reflog = f.git(t, f.repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", tree, "-p", f.commit, "-m", "reflog-only")
	for _, values := range [][]string{{m.reflog, f.commit}, {f.commit, m.reflog}} {
		f.git(t, f.checkout, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "update-ref", "-m", "owned prior observation", "refs/heads/task", values[0], values[1])
	}
	if err := os.Rename(f.checkout, m.saved); err != nil {
		t.Fatal(err)
	}
	m.m = MissingManager{Git: f.m.Git, VerifyStorage: func(ctx context.Context, raw json.RawMessage) error {
		if string(raw) != `{"proof":"fixture"}` {
			return errors.New("wrong storage proof")
		}
		return ctx.Err()
	}, Gate: func(ctx context.Context, p MissingPlan) error { m.gateCalls++; return ctx.Err() }}
	p, err := m.m.Inspect(context.Background(), MissingSpec{ID: "missing1", Checkout: f.checkout, Common: f.repo, TaskRef: "refs/heads/task", Commit: f.commit, Storage: json.RawMessage(`{"proof":"fixture"}`)})
	if err != nil {
		t.Fatal(err)
	}
	m.plan = p
	return m
}
func TestMissingQuarantineRetainsIndexReflogAndOriginalTaskRef(t *testing.T) {
	f := newMissingFixture(t)
	ctx := context.Background()
	p := f.plan
	if p.MissingAt != f.checkout || p.Parent != f.root || p.AdminBytes == 0 {
		t.Fatal("incorrect missing observation")
	}
	objects := map[string]bool{}
	for _, o := range p.Objects {
		objects[o.OID] = true
	}
	if !objects[f.staged] || !objects[f.reflog] {
		t.Fatal("unpublished objects not protected", objects)
	}
	if err := f.m.Preflight(ctx, p); err != nil {
		t.Fatal(err)
	}
	if f.gateCalls != 0 {
		t.Fatal("readonly preflight used mutation gate")
	}
	hook := filepath.Join(f.repo, "hooks", "reference-transaction")
	marker := filepath.Join(f.root, "forbidden-hook")
	write(t, hook, "#!/bin/sh\nprintf unsafe > "+marker+"\n")
	if err := os.Chmod(hook, 0700); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("arbitrary hook ran")
	}
	evidence, err := f.m.loadEvidence(ctx, p)
	if err != nil || evidence == nil {
		t.Fatal("retained evidence", err)
	}
	if err = f.m.Preflight(ctx, p); err != nil {
		t.Fatal("completed preflight", err)
	}
	if err = f.m.Apply(ctx, p); err != nil {
		t.Fatal("exact retry", err)
	}
	head, err := os.ReadFile(filepath.Join(p.Admin, "HEAD"))
	if err != nil || string(head) != p.Spec.Commit+"\n" {
		t.Fatal("missing source not detached", err)
	}
	if f.git(t, f.repo, "rev-parse", p.Spec.TaskRef) != p.Spec.Commit {
		t.Fatal("task ref changed")
	}
	if raw, err := os.ReadFile(filepath.Join(f.saved, "tracked")); err != nil || string(raw) != "unpublished staged bytes\n" {
		t.Fatal("preserved checkout touched", err)
	}
	destination := filepath.Join(f.root, "destination")
	f.git(t, f.repo, "worktree", "add", destination, "task")
	if f.git(t, destination, "symbolic-ref", "HEAD") != p.Spec.TaskRef {
		t.Fatal("original task ref not usable")
	}
	if err = f.m.VerifyRetained(ctx, p); err != nil {
		t.Fatal("retained source proof", err)
	}
	f.git(t, f.repo, "-c", "core.hooksPath=/dev/null", "update-ref", p.Spec.TaskRef, f.reflog, p.Spec.Commit)
	if err = f.m.VerifyRetained(ctx, p); err != nil {
		t.Fatal("destination advancement invalidated retained proof", err)
	}
	if err = f.m.Preflight(ctx, p); err == nil {
		t.Fatal("pre-relinquish approval accepted advanced original tip")
	}
	// Object refs protect evidence independently of reflog expiry and automatic
	// worktree pruning. This operates only on the disposable fixture repository.
	f.git(t, f.repo, "-c", "core.hooksPath=/dev/null", "reflog", "expire", "--expire=now", "--all")
	f.git(t, f.repo, "-c", "core.hooksPath=/dev/null", "gc", "--prune=now")
	for _, value := range []string{f.staged, f.reflog} {
		if f.git(t, f.repo, "cat-file", "-t", value) == "" {
			t.Fatal("retained object lost")
		}
	}
	if _, err = os.Stat(filepath.Join(p.Admin, "index")); err != nil {
		t.Fatal("locked missing administration was pruned", err)
	}
	if _, err = f.m.loadEvidence(ctx, p); err != nil {
		t.Fatal("maintenance changed private evidence", err)
	}
}
func TestMissingQuarantineFaultRetry(t *testing.T) {
	for _, point := range []string{"stage-before", "evidence", "pin-before", "pin-after", "lock", "head-before", "head-after"} {
		t.Run(point, func(t *testing.T) {
			f := newMissingFixture(t)
			baseGit, baseGate := f.m.Git, f.m.Gate
			fault := errors.New("injected boundary")
			fired := false
			f.m.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
				isUpdate := false
				isHead := false
				for _, arg := range args {
					if arg == "update-ref" {
						isUpdate = true
					}
					if arg == "HEAD" {
						isHead = true
					}
				}
				if isUpdate && !fired && (strings.HasPrefix(point, "pin-") && !isHead || strings.HasPrefix(point, "head-") && isHead) {
					fired = true
					if strings.HasSuffix(point, "before") {
						return "", fault
					}
					out, err := baseGit(ctx, dir, args...)
					return out, errors.Join(err, fault)
				}
				return baseGit(ctx, dir, args...)
			}
			f.m.Gate = func(ctx context.Context, p MissingPlan) error {
				if err := baseGate(ctx, p); err != nil {
					return err
				}
				if !fired {
					if point == "stage-before" {
						if stages, _ := filepath.Glob(filepath.Join(filepath.Dir(p.Evidence), "data-*")); len(stages) > 0 {
							if _, err := os.Lstat(p.Evidence); os.IsNotExist(err) {
								fired = true
								return fault
							}
						}
					}
					if point == "evidence" {
						if _, err := os.Lstat(p.Evidence); err == nil {
							fired = true
							return fault
						}
					}
					if point == "lock" {
						if _, err := os.Lstat(filepath.Join(p.Admin, "locked")); err == nil {
							fired = true
							return fault
						}
					}
				}
				return nil
			}
			if err := f.m.Apply(context.Background(), f.plan); !errors.Is(err, fault) || !fired {
				t.Fatal("fault not reached", err)
			}
			f.m.Git, f.m.Gate = baseGit, baseGate
			if err := f.m.Apply(context.Background(), f.plan); err != nil {
				t.Fatal("exact-owned retry failed", err)
			}
			if err := f.m.Preflight(context.Background(), f.plan); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestMissingRejectsReappearanceAndChangedEvidence(t *testing.T) {
	for _, kind := range []string{"checkout", "dangling", "parent", "admin", "index", "locked", "unknown-ref", "unowned-matching-ref", "evidence", "copied-file", "unknown-stage-file", "lock-inode", "storage", "nil-gate"} {
		t.Run(kind, func(t *testing.T) {
			f := newMissingFixture(t)
			p := f.plan
			ctx := context.Background()
			if kind == "evidence" || kind == "copied-file" || kind == "unknown-stage-file" || kind == "lock-inode" {
				if err := f.m.Apply(ctx, p); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "checkout":
				if err := os.Mkdir(f.checkout, 0700); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(f.checkout, "keep"), "returned checkout")
			case "dangling":
				if err := os.Symlink(filepath.Join(f.root, "never-existed"), f.checkout); err != nil {
					t.Fatal(err)
				}
			case "parent":
				p.Anchors[len(p.Anchors)-1].Identity.Inode++
			case "admin":
				old := p.Admin + "-saved"
				if err := os.Rename(p.Admin, old); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p.Admin, 0700); err != nil {
					t.Fatal(err)
				}
			case "index":
				write(t, filepath.Join(p.Admin, "index"), "changed index")
			case "locked":
				write(t, filepath.Join(p.Admin, "locked"), "operator-owned lock")
			case "unknown-ref":
				f.git(t, f.repo, "update-ref", f.m.refPrefix(p)+"unknown", f.commit)
			case "unowned-matching-ref":
				f.git(t, f.repo, "update-ref", f.m.refPrefix(p)+p.Objects[0].OID, p.Objects[0].OID)
			case "evidence":
				write(t, p.Evidence, `{"unknown":"do not overwrite"}`)
			case "copied-file":
				e, err := f.m.loadEvidence(ctx, p)
				if err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(filepath.Dir(p.Evidence), e.Stage, "admin/index"), "changed retained bytes")
			case "unknown-stage-file":
				e, err := f.m.loadEvidence(ctx, p)
				if err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(filepath.Dir(p.Evidence), e.Stage, "unknown"), "do not delete")
			case "lock-inode":
				path := filepath.Join(p.Admin, "locked")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				write(t, path, string(raw))
			case "storage":
				f.m.VerifyStorage = func(context.Context, json.RawMessage) error { return errors.New("volume unavailable") }
			case "nil-gate":
				f.m.Gate = nil
			}
			before, _ := os.ReadFile(filepath.Join(p.Admin, "HEAD"))
			if err := f.m.Apply(ctx, p); err == nil {
				t.Fatal("unsafe approval accepted")
			}
			after, _ := os.ReadFile(filepath.Join(p.Admin, "HEAD"))
			if string(before) != string(after) {
				t.Fatal("refused operation changed HEAD")
			}
			if kind == "checkout" {
				raw, err := os.ReadFile(filepath.Join(f.checkout, "keep"))
				if err != nil || string(raw) != "returned checkout" {
					t.Fatal("returned checkout touched", err)
				}
			}
		})
	}
}
func TestMissingWholePreflightAndLateReappearance(t *testing.T) {
	f := newMissingFixture(t)
	ctx := context.Background()
	bad := f.plan
	bad.Spec.Storage = json.RawMessage(`{"proof":"wrong"}`)
	if err := f.m.Preflight(ctx, f.plan); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Preflight(ctx, bad); err == nil {
		t.Fatal("bad later member accepted")
	}
	if f.gateCalls != 0 {
		t.Fatal("preflight had effects")
	}
	if _, err := os.Lstat(f.plan.Evidence); !os.IsNotExist(err) {
		t.Fatal("preflight published evidence")
	}
	base := f.m.Gate
	f.m.Gate = func(c context.Context, p MissingPlan) error {
		if err := base(c, p); err != nil {
			return err
		}
		if _, err := os.Lstat(p.Evidence); err == nil {
			if err = os.Mkdir(f.checkout, 0700); err != nil && !os.IsExist(err) {
				return err
			}
			write(t, filepath.Join(f.checkout, "keep"), "late return")
		}
		return nil
	}
	if err := f.m.Apply(ctx, f.plan); err == nil {
		t.Fatal("late checkout accepted")
	}
	head, err := os.ReadFile(filepath.Join(f.plan.Admin, "HEAD"))
	if err != nil || string(head) != "ref: refs/heads/task\n" {
		t.Fatal("late return still detached", err)
	}
	raw, err := os.ReadFile(filepath.Join(f.checkout, "keep"))
	if err != nil || string(raw) != "late return" {
		t.Fatal("late files touched", err)
	}
}
func TestMissingPlanRoundtripAndStorageAmbiguity(t *testing.T) {
	f := newMissingFixture(t)
	raw, err := json.Marshal(f.plan)
	if err != nil {
		t.Fatal(err)
	}
	var p MissingPlan
	if err = json.Unmarshal(raw, &p); err != nil || !reflect.DeepEqual(p, f.plan) || p.Digest() != f.plan.Digest() {
		t.Fatal("plan not stable", err)
	}
	for _, proof := range []string{`{"proof":"fixture","proof":"fixture"}`, `{"proof":"fixture"} `, `null`, `{"nested":{"a":1,"a":1}}`} {
		s := f.plan.Spec
		s.Storage = json.RawMessage(proof)
		if _, err = f.m.Inspect(context.Background(), s); err == nil {
			t.Fatal("ambiguous proof accepted")
		}
	}
}

func TestMissingDescriptorLeafAndInitialRegistrationGuards(t *testing.T) {
	f := newMissingFixture(t)
	anchors, missing, parent, err := missingAnchors(filepath.Join(f.checkout, "nested/member"))
	if err != nil || missing != f.checkout || parent != f.root || len(anchors) == 0 {
		t.Fatal("wrong first absent leaf", missing, parent, err)
	}
	alias := filepath.Join(f.root, "alias")
	if err = os.Symlink(f.root, alias); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = missingAnchors(filepath.Join(alias, "missing")); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
	write(t, filepath.Join(f.root, "not-directory"), "preserve")
	if _, _, _, err = missingAnchors(filepath.Join(f.root, "not-directory/leaf")); err == nil {
		t.Fatal("ENOTDIR treated as absence")
	}
	write(t, filepath.Join(f.plan.Admin, "locked"), "operator lock")
	if _, err = f.m.Inspect(context.Background(), f.plan.Spec); err == nil {
		t.Fatal("preexisting lock accepted")
	}
	if err = os.Rename(filepath.Join(f.plan.Admin, "locked"), filepath.Join(f.root, "preserved-operator-lock")); err != nil {
		t.Fatal(err)
	}
	duplicate := filepath.Join(f.repo, "worktrees", "duplicate")
	if err = os.Mkdir(duplicate, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HEAD", "gitdir"} {
		raw, e := os.ReadFile(filepath.Join(f.plan.Admin, name))
		if e != nil {
			t.Fatal(e)
		}
		write(t, filepath.Join(duplicate, name), string(raw))
	}
	if _, err = f.m.Inspect(context.Background(), f.plan.Spec); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	if _, err = os.Lstat(f.plan.Evidence); !os.IsNotExist(err) {
		t.Fatal("refused inspection created evidence")
	}
}
func TestMissingCanceledGateHasNoEffects(t *testing.T) {
	f := newMissingFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.m.Apply(ctx, f.plan); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(f.plan.Evidence); !os.IsNotExist(err) {
		t.Fatal("canceled operation created evidence")
	}
}

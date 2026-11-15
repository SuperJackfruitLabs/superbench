package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"os"
	"path/filepath"
	"testing"
)

func materializationFixture(t *testing.T) (Planner, Plan) {
	t.Helper()
	root := t.TempDir()
	a, b := remote(t, root, "a", ""), remote(t, root, "b", "")
	p := planner(t, root)
	plan, err := p.Select(context.Background(), Spec{Key: "card", CheckpointRepository: "a", Repositories: []Repository{{Name: "a", Repository: a}, {Name: "b", Repository: b}}})
	if err != nil {
		t.Fatal(err)
	}
	return p, plan
}
func allowMaterialization(context.Context, Plan) error { return nil }
func assertPinned(t *testing.T, plan Plan) {
	t.Helper()
	for _, member := range plan.Members {
		if got := git(t, member.Path, "rev-parse", "HEAD"); got != member.Source {
			t.Fatalf("%s: %s != %s", member.Name, got, member.Source)
		}
		if got := git(t, member.Path, "symbolic-ref", "HEAD"); got != "refs/heads/"+member.Branch {
			t.Fatalf("wrong branch: %s", got)
		}
	}
}
func TestMaterializeWholeVectorAndAdoptExactRetry(t *testing.T) {
	p, plan := materializationFixture(t)
	gates := 0
	m := Materializer{Git: p.Git, Gate: func(_ context.Context, actual Plan) error {
		gates++
		if actual.Root != plan.Root || len(actual.Members) != 2 {
			t.Fatal("gate received changed plan")
		}
		return nil
	}}
	if err := m.Materialize(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	assertPinned(t, plan)
	if err := m.Materialize(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	assertPinned(t, plan)
	if gates != 6 {
		t.Fatalf("ownership gates: %d", gates)
	}
}
func TestMaterializeSecondFailurePreservesFirstAndPinnedRetry(t *testing.T) {
	p, plan := materializationFixture(t)
	failed := false
	m := Materializer{Gate: allowMaterialization, Git: func(ctx context.Context, dir string, args ...string) (string, error) {
		if !failed && dir == plan.Members[1].Mirror && len(args) > 1 && args[0] == "worktree" && args[1] == "add" {
			failed = true
			return "", errors.New("injected second checkout failure")
		}
		return p.Git(ctx, dir, args...)
	}}
	if err := m.Materialize(context.Background(), plan); err == nil || !failed {
		t.Fatal("second checkout did not fail")
	}
	if got := git(t, plan.Members[0].Path, "rev-parse", "HEAD"); got != plan.Members[0].Source {
		t.Fatal("first checkout lost")
	}
	if _, err := os.Lstat(plan.Members[1].Path); !os.IsNotExist(err) {
		t.Fatalf("unexpected second checkout: %v", err)
	}
	git(t, plan.Members[1].Repository, "commit", "--allow-empty", "-m", "later remote tip")
	if _, err := p.Mirrors.Prepare(context.Background(), plan.Members[1].Repository); err != nil {
		t.Fatal(err)
	}
	if err := m.Materialize(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	assertPinned(t, plan)
}
func TestMaterializeRejectsDirtyFirstBeforeCreatingSecond(t *testing.T) {
	p, plan := materializationFixture(t)
	m := Materializer{Git: p.Git, Gate: allowMaterialization}
	m.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		if dir == plan.Members[1].Mirror && len(args) > 1 && args[0] == "worktree" && args[1] == "add" {
			return "", errors.New("stop after first")
		}
		return p.Git(ctx, dir, args...)
	}
	if err := m.Materialize(context.Background(), plan); err == nil {
		t.Fatal("missing failure")
	}
	dirty := filepath.Join(plan.Members[0].Path, "untracked")
	if err := os.WriteFile(dirty, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	m.Git = p.Git
	if err := m.Materialize(context.Background(), plan); err == nil {
		t.Fatal("dirty checkout adopted")
	}
	if raw, err := os.ReadFile(dirty); err != nil || string(raw) != "keep" {
		t.Fatal("dirty work modified")
	}
	if _, err := os.Lstat(plan.Members[1].Path); !os.IsNotExist(err) {
		t.Fatalf("second checkout created: %v", err)
	}
}
func TestMaterializePreflightsConflictBeforeFirstEffect(t *testing.T) {
	p, plan := materializationFixture(t)
	second := plan.Members[1]
	git(t, second.Repository, "commit", "--allow-empty", "-m", "conflict")
	if _, err := p.Mirrors.Prepare(context.Background(), second.Repository); err != nil {
		t.Fatal(err)
	}
	later := git(t, second.Repository, "rev-parse", "HEAD")
	git(t, second.Mirror, "branch", second.Branch, later)
	m := Materializer{Git: p.Git, Gate: allowMaterialization}
	if err := m.Materialize(context.Background(), plan); err == nil {
		t.Fatal("conflict accepted")
	}
	if _, err := os.Lstat(plan.Root); !os.IsNotExist(err) {
		t.Fatalf("first checkout created before full preflight: %v", err)
	}
	if got := git(t, second.Mirror, "rev-parse", "refs/heads/"+second.Branch); got != later {
		t.Fatal("conflicting branch reset")
	}
}
func TestMaterializeRejectsSymlinksAndUnknownEntries(t *testing.T) {
	for _, target := range []string{"root", "member", "mirror", "unknown"} {
		t.Run(target, func(t *testing.T) {
			p, plan := materializationFixture(t)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "keep")
			if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(plan.Root), 0700); err != nil {
				t.Fatal(err)
			}
			switch target {
			case "root":
				if err := os.Symlink(outside, plan.Root); err != nil {
					t.Fatal(err)
				}
			case "member":
				if err := os.Mkdir(plan.Root, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, plan.Members[0].Path); err != nil {
					t.Fatal(err)
				}
			case "mirror":
				original := plan.Members[1].Mirror
				if err := os.Rename(original, original+"-preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original+"-preserved", original); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if err := os.Mkdir(plan.Root, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(plan.Root, "unknown"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := (Materializer{Git: p.Git, Gate: allowMaterialization}).Materialize(context.Background(), plan); err == nil {
				t.Fatal("unsafe storage accepted")
			}
			if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "keep" {
				t.Fatal("external data touched")
			}
		})
	}
}
func TestMaterializeRequiresGateAndRejectsChangedPlanWithoutEffects(t *testing.T) {
	p, plan := materializationFixture(t)
	calls := 0
	counted := func(ctx context.Context, dir string, args ...string) (string, error) {
		calls++
		return p.Git(ctx, dir, args...)
	}
	if err := (Materializer{Git: counted}).Materialize(context.Background(), plan); err == nil {
		t.Fatal("missing gate accepted")
	}
	for _, change := range []func(*Plan){
		func(p *Plan) { p.Root = filepath.Dir(p.Root) },
		func(p *Plan) { p.Members[1].Source = "not-a-commit" },
		func(p *Plan) { p.Members[1].Branch = "" },
		func(p *Plan) { p.Members[1].Path = p.Members[0].Path },
		func(p *Plan) { p.Members[1].Mirror = p.Members[0].Mirror },
		func(p *Plan) { p.Members[1].ObjectFormat = "sha256" },
	} {
		altered := plan
		altered.Members = append([]Member(nil), plan.Members...)
		change(&altered)
		if err := (Materializer{Git: counted, Gate: allowMaterialization}).Materialize(context.Background(), altered); err == nil {
			t.Fatal("changed plan accepted")
		}
	}
	if calls != 0 {
		t.Fatalf("invalid plan executed Git: %d", calls)
	}
	if _, err := os.Lstat(plan.Root); !os.IsNotExist(err) {
		t.Fatalf("invalid plan created root: %v", err)
	}
	blocked := errors.New("no longer initialization owner")
	if err := (Materializer{Git: counted, Gate: func(context.Context, Plan) error { return blocked }}).Materialize(context.Background(), plan); !errors.Is(err, blocked) {
		t.Fatalf("gate rejection ignored: %v", err)
	}
}

func TestMaterializeBoundsGateAndGitContexts(t *testing.T) {
	_, plan := materializationFixture(t)
	for _, parentShorter := range []bool{false, true} {
		t.Run(fmt.Sprint("parentShorter=", parentShorter), func(t *testing.T) {
			ctx := context.Background()
			timeout := time.Second
			var parentDeadline time.Time
			if parentShorter {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 25*time.Millisecond)
				defer cancel()
				parentDeadline, _ = ctx.Deadline()
				timeout = time.Hour
			}
			var gateDeadline time.Time
			calls := 0
			m := Materializer{Timeout: timeout, Gate: func(ctx context.Context, _ Plan) error {
				var ok bool
				gateDeadline, ok = ctx.Deadline()
				if !ok {
					t.Fatal("gate context is unbounded")
				}
				if parentShorter && !gateDeadline.Equal(parentDeadline) {
					t.Fatal("shorter parent deadline was extended")
				}
				return nil
			}, Git: func(ctx context.Context, _ string, _ ...string) (string, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || !deadline.Equal(gateDeadline) {
					t.Fatal("Git did not inherit whole-operation deadline")
				}
				<-ctx.Done()
				return "", ctx.Err()
			}}
			if err := m.Materialize(ctx, plan); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline ignored: %v", err)
			}
			if calls != 1 {
				t.Fatalf("unexpected Git calls: %d", calls)
			}
			if _, err := os.Lstat(plan.Root); !os.IsNotExist(err) {
				t.Fatalf("deadline created checkout: %v", err)
			}
		})
	}
}

func TestMaterializeRejectsInvalidTimeoutAndCanceledParentBeforeEffects(t *testing.T) {
	_, plan := materializationFixture(t)
	calls := 0
	m := Materializer{Gate: func(context.Context, Plan) error { calls++; return nil }, Git: func(context.Context, string, ...string) (string, error) { calls++; return "", nil }}
	for _, timeout := range []time.Duration{-time.Nanosecond, time.Hour + time.Nanosecond} {
		m.Timeout = timeout
		if err := m.Materialize(context.Background(), plan); err == nil {
			t.Fatal("invalid timeout accepted")
		}
	}
	m.Timeout = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Materialize(ctx, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parent ignored: %v", err)
	}
	if calls != 0 {
		t.Fatalf("invalid/canceled operation caused effects: %d", calls)
	}
	// The default bound applies to the ownership gate too, before any Git call.
	blocked := errors.New("gate probe")
	m.Gate = func(ctx context.Context, _ Plan) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > repository.DefaultTimeout {
			t.Fatal("missing default materialization bound")
		}
		return blocked
	}
	if err := m.Materialize(context.Background(), plan); !errors.Is(err, blocked) {
		t.Fatalf("gate probe: %v", err)
	}
}

func TestMaterializeRejectsCrossMemberBranchNamespaceBeforeEffects(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint("reverse=", reverse), func(t *testing.T) {
			p, plan := materializationFixture(t)
			first := plan.Members[0]
			plan.Members[1].Repository = first.Repository
			plan.Members[1].Mirror = first.Mirror
			plan.Members[1].Source = first.Source
			plan.Members[1].Branch = first.Branch + "/child"
			if reverse {
				plan.Members[0].Branch, plan.Members[1].Branch = plan.Members[1].Branch, plan.Members[0].Branch
			}
			calls := 0
			m := Materializer{Git: func(ctx context.Context, dir string, args ...string) (string, error) {
				calls++
				return p.Git(ctx, dir, args...)
			}, Gate: allowMaterialization}
			if err := m.Materialize(context.Background(), plan); err == nil {
				t.Fatal("cross-member branch namespace conflict accepted")
			}
			if calls != 0 {
				t.Fatal("pure plan conflict executed Git")
			}
			if _, err := os.Lstat(plan.Root); !os.IsNotExist(err) {
				t.Fatalf("workspace root created: %v", err)
			}
			if got := git(t, first.Mirror, "for-each-ref", "--format=%(refname)", "refs/heads/bench/"); got != "" {
				t.Fatalf("task refs created: %s", got)
			}
		})
	}
}

func TestMaterializePreflightsAllExistingBranchNamespaces(t *testing.T) {
	for _, existing := range []string{"bench", "bench/card/child"} {
		t.Run(existing, func(t *testing.T) {
			p, plan := materializationFixture(t)
			second := plan.Members[1]
			git(t, second.Mirror, "branch", existing, second.Source)
			git(t, second.Mirror, "branch", "unrelated", second.Source)
			if err := (Materializer{Git: p.Git, Gate: allowMaterialization}).Materialize(context.Background(), plan); err == nil {
				t.Fatal("existing branch namespace conflict accepted")
			}
			if _, err := os.Lstat(plan.Root); !os.IsNotExist(err) {
				t.Fatalf("first checkout/root created: %v", err)
			}
			for _, ref := range []string{existing, "unrelated"} {
				if got := git(t, second.Mirror, "rev-parse", "refs/heads/"+ref); got != second.Source {
					t.Fatalf("existing ref %s changed", ref)
				}
			}
		})
	}
}

func TestMaterializePreflightsSameCommitBranchOwnedElsewhere(t *testing.T) {
	p, plan := materializationFixture(t)
	second := plan.Members[1]
	other := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(plan.Root))), "other checkout\nwith newline")
	git(t, second.Mirror, "worktree", "add", "-b", second.Branch, other, second.Source)
	sentinel := filepath.Join(other, "keep")
	if err := os.WriteFile(sentinel, []byte("unrelated work"), 0600); err != nil {
		t.Fatal(err)
	}
	before := git(t, second.Mirror, "worktree", "list", "--porcelain", "-z")
	if err := (Materializer{Git: p.Git, Gate: allowMaterialization}).Materialize(context.Background(), plan); err == nil {
		t.Fatal("branch owned elsewhere accepted")
	}
	if _, err := os.Lstat(plan.Root); !os.IsNotExist(err) {
		t.Fatalf("first checkout/root created: %v", err)
	}
	if after := git(t, second.Mirror, "worktree", "list", "--porcelain", "-z"); after != before {
		t.Fatal("worktree registrations changed")
	}
	if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "unrelated work" {
		t.Fatal("unrelated work changed")
	}
	if got := git(t, other, "rev-parse", "HEAD"); got != second.Source {
		t.Fatal("other checkout moved")
	}
}

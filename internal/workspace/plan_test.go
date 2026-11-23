package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitcontrol.Command(context.Background(), dir, args...)
	cmd.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", args[0], err, out)
	}
	return strings.TrimSpace(string(out))
}

func remote(t *testing.T, root, name, quota string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-b", "main")
	if quota != "" {
		if err := os.Mkdir(filepath.Join(dir, ".bench"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".bench", "config.toml"), []byte("[quota]\nsize = \""+quota+"\"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "--allow-empty", "-m", "initial")
	return dir
}

func planner(t *testing.T, root string) Planner {
	t.Helper()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	return Planner{Mirrors: repository.Mirrors{State: state, Data: filepath.Join(root, "data")}, Git: func(ctx context.Context, dir string, args ...string) (string, error) {
		cmd := gitcontrol.Command(ctx, dir, args...)
		cmd.Env = gitcontrol.Environment(os.Environ())
		out, err := cmd.Output()
		return string(out), err
	}}
}

func TestSelectPinsWholeVectorBeforeCheckoutEffects(t *testing.T) {
	root := t.TempDir()
	a, b := remote(t, root, "a", "1GiB"), remote(t, root, "b", "1GiB")
	git(t, a, "checkout", "-b", "bench/card")
	git(t, a, "commit", "--allow-empty", "-m", "task")
	wantA, wantB := git(t, a, "rev-parse", "HEAD"), git(t, b, "rev-parse", "HEAD")
	p := planner(t, root)
	plan, err := p.Select(context.Background(), Spec{Key: "card", CheckpointRepository: "a", Repositories: []Repository{{Name: "a", Repository: a}, {Name: "b", Repository: b}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Members) != 2 || plan.Members[0].Source != wantA || plan.Members[1].Source != wantB || plan.QuotaBytes != 1<<30 {
		t.Fatalf("unexpected vector/budget: %+v", plan)
	}
	for _, member := range plan.Members {
		if member.Path != filepath.Join(plan.Root, member.Name) || member.RepositoryQuotaBytes != 1<<30 || member.ObjectFormat != "sha1" {
			t.Fatalf("unexpected member: %+v", member)
		}
	}
	if _, err := os.Lstat(plan.Root); !os.IsNotExist(err) {
		t.Fatalf("planning created checkout root: %v", err)
	}
	// A later source change cannot mutate the already selected vector.
	git(t, b, "commit", "--allow-empty", "-m", "later")
	if plan.Members[1].Source != wantB {
		t.Fatal("source vector changed after selection")
	}
}

func TestSecondSourceFailurePublishesNoPartialPlan(t *testing.T) {
	root := t.TempDir()
	a, b := remote(t, root, "a", ""), remote(t, root, "b", "")
	p := planner(t, root)
	plan, err := p.Select(context.Background(), Spec{Key: "card", CheckpointRepository: "a", Repositories: []Repository{{Name: "a", Repository: a}, {Name: "b", Repository: b, Base: "missing"}}})
	if err == nil || len(plan.Members) != 0 || plan.Root != "" {
		t.Fatalf("partial plan escaped: %+v, %v", plan, err)
	}
	if _, err := os.Lstat(filepath.Join(p.Mirrors.Data, "benches")); !os.IsNotExist(err) {
		t.Fatalf("failed planning created checkout parent: %v", err)
	}
}

func TestQuotaDeclarationsNeverMultiplyBudget(t *testing.T) {
	root := t.TempDir()
	a, b := remote(t, root, "a", "1GiB"), remote(t, root, "b", "2GiB")
	p := planner(t, root)
	s := Spec{Key: "card", CheckpointRepository: "a", Repositories: []Repository{{Name: "a", Repository: a}, {Name: "b", Repository: b}}}
	if _, err := p.Select(context.Background(), s); err == nil {
		t.Fatal("conflicting declarations were accepted")
	}
	s.QuotaBytes = 3 << 30
	if _, err := p.Select(context.Background(), s); err == nil {
		t.Fatal("explicit aggregate silently overrode source declarations")
	}
}

func TestInvalidSpecHasNoMirrorEffects(t *testing.T) {
	root := t.TempDir()
	p := planner(t, root)
	valid := Spec{Key: "card", CheckpointRepository: "a", Repositories: []Repository{{Name: "a", Repository: "/first"}, {Name: "b", Repository: "/second"}}}
	for _, change := range []func(*Spec){
		func(s *Spec) { s.Key = "../escape" },
		func(s *Spec) { s.CheckpointRepository = "absent" },
		func(s *Spec) { s.Repositories[1].Name = "a" },
		func(s *Spec) { s.Repositories[1].Repository = "/first" },
		func(s *Spec) { s.Repositories[1].Branch = "HEAD" },
		func(s *Spec) { s.Repositories[1].Base = "refs/heads/main" },
		func(s *Spec) { s.Repositories[1].Repository = "https://user:password@example.invalid/repo" },
		func(s *Spec) { s.QuotaBytes = 1025 },
	} {
		s := valid
		s.Repositories = append([]Repository(nil), valid.Repositories...)
		change(&s)
		if _, err := p.Select(context.Background(), s); err == nil {
			t.Fatal("invalid spec accepted")
		}
	}
	if _, err := os.Lstat(p.Mirrors.Data); !os.IsNotExist(err) {
		t.Fatalf("validation caused storage effects: %v", err)
	}
}

// expiringContext is a caller context whose expiry the test triggers once the
// step under test is reached. A wall-clock deadline short enough to fire there
// also fires during the real git and database setup before it when the host is
// loaded, so it cannot prove which step the deadline covered.
type expiringContext struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newExpiringContext() *expiringContext {
	return &expiringContext{Context: context.Background(), done: make(chan struct{})}
}

func (c *expiringContext) Deadline() (time.Time, bool) { return time.Now().Add(time.Hour), true }
func (c *expiringContext) Done() <-chan struct{}       { return c.done }
func (c *expiringContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (c *expiringContext) expire() { c.once.Do(func() { close(c.done) }) }

// boundedBy fails unless ctx carries the deadline timeout after a moment
// between from and to: the bound under test, rather than the caller's distant
// one, none at all, or a fresh one started later.
func boundedBy(t *testing.T, ctx context.Context, from, to time.Time, timeout time.Duration) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("unbounded context")
	}
	if deadline.Before(from.Add(timeout)) || deadline.After(to.Add(timeout)) {
		t.Fatalf("deadline %v is not %v after a moment between %v and %v", deadline, timeout, from, to)
	}
}

func TestSourceObservationInheritsBoundedMemberContext(t *testing.T) {
	root := t.TempDir()
	a := remote(t, root, "a", "")
	p := planner(t, root)
	// Long enough for the real mirror clone on a loaded host, and below the
	// two-minute default so the deadline check proves this bound applies.
	p.Mirrors.Timeout = time.Minute
	caller := newExpiringContext()
	start := time.Now()
	called := false
	p.Git = func(ctx context.Context, _ string, _ ...string) (string, error) {
		called = true
		boundedBy(t, ctx, start, time.Now(), p.Mirrors.Timeout)
		caller.expire()
		<-ctx.Done()
		return "", ctx.Err()
	}
	plan, err := p.Select(caller, Spec{Key: "card", CheckpointRepository: "a", Repositories: []Repository{{Name: "a", Repository: a}}})
	if !called || !errors.Is(err, context.DeadlineExceeded) || len(plan.Members) != 0 {
		t.Fatalf("deadline did not cover source observation: %v, %v", called, err)
	}
}

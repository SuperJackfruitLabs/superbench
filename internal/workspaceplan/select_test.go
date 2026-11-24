package workspaceplan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/deadlinetest"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func selectRemote(t *testing.T, root, name, quota string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	inspectGit(t, dir, "init", "-b", "main")
	if quota != "" {
		if err := os.Mkdir(filepath.Join(dir, ".bench"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".bench", "config.toml"), []byte("[quota]\nsize = \""+quota+"\"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	inspectGit(t, dir, "add", ".")
	inspectGit(t, dir, "commit", "--allow-empty", "-m", "initial")
	return dir
}

func selectPlanner(t *testing.T, root string) Planner {
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
	a, b := selectRemote(t, root, "a", "1GiB"), selectRemote(t, root, "b", "1GiB")
	inspectGit(t, a, "checkout", "-b", "bench/card")
	inspectGit(t, a, "commit", "--allow-empty", "-m", "task")
	wantA, wantB := inspectGit(t, a, "rev-parse", "HEAD"), inspectGit(t, b, "rev-parse", "HEAD")
	p := selectPlanner(t, root)
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
	inspectGit(t, b, "commit", "--allow-empty", "-m", "later")
	if plan.Members[1].Source != wantB {
		t.Fatal("source vector changed after selection")
	}
}

func TestSecondSourceFailurePublishesNoPartialPlan(t *testing.T) {
	root := t.TempDir()
	a, b := selectRemote(t, root, "a", ""), selectRemote(t, root, "b", "")
	p := selectPlanner(t, root)
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
	a, b := selectRemote(t, root, "a", "1GiB"), selectRemote(t, root, "b", "2GiB")
	p := selectPlanner(t, root)
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
	p := selectPlanner(t, root)
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

func TestSourceObservationInheritsBoundedMemberContext(t *testing.T) {
	root := t.TempDir()
	a := selectRemote(t, root, "a", "")
	p := selectPlanner(t, root)
	p.Mirrors.Timeout = time.Minute // enough for the real mirror under load, below the default
	caller, start, called := deadlinetest.New(), time.Now(), false
	p.Git = func(ctx context.Context, _ string, _ ...string) (string, error) {
		called = true
		deadlinetest.Bounded(t, ctx, start, time.Now(), p.Mirrors.Timeout)
		caller.Expire()
		<-ctx.Done()
		return "", ctx.Err()
	}
	plan, err := p.Select(caller, Spec{Key: "card", CheckpointRepository: "a", Repositories: []Repository{{Name: "a", Repository: a}}})
	if !called || !errors.Is(err, context.DeadlineExceeded) || len(plan.Members) != 0 {
		t.Fatalf("deadline did not cover source observation: %v, %v", called, err)
	}
}

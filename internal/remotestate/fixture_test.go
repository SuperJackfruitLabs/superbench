package remotestate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

// scopes records every scope call. A created scope is present until fenced
// or until a test removes it, as a benchd restart does.
type scopes struct {
	mu      sync.Mutex
	calls   []string
	created []string
	present map[string]bool
	fail    error
	// onCreate and onFence run outside the lock, after the call is recorded.
	onCreate, onFence func()
}

func (s *scopes) Create(root, id string) (execution.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "create:"+id)
	s.created = append(s.created, id)
	if s.present == nil {
		s.present = map[string]bool{}
	}
	s.present[id] = true
	if hook := s.onCreate; hook != nil {
		s.mu.Unlock()
		hook()
		s.mu.Lock()
	}
	return execution.Identity{BootID: "test-boot", HostID: "test-host"}, nil
}
func (s *scopes) Fence(e *execution.Execution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "fence:"+e.ID)
	if s.fail != nil {
		return s.fail
	}
	delete(s.present, e.ID)
	if hook := s.onFence; hook != nil {
		s.onFence = nil
		s.mu.Unlock()
		hook()
		s.mu.Lock()
	}
	return nil
}
func (s *scopes) Empty(e *execution.Execution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.present[e.ID] {
		return errors.New("scope still populated")
	}
	return nil
}
func (s *scopes) Present(e *execution.Execution) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "present:"+e.ID)
	return s.present[e.ID], nil
}

// Start lets the fake stand in for workspaceprep's backend; it runs nothing.
func (s *scopes) Start(*exec.Cmd, *execution.Execution) error {
	return errors.New("fixture starts no process")
}
func (s *scopes) gone(id string) { s.mu.Lock(); delete(s.present, id); s.mu.Unlock() }
func (s *scopes) log() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}
func (s *scopes) creates() int {
	n := 0
	for _, c := range s.log() {
		if strings.HasPrefix(c, "create:") {
			n++
		}
	}
	return n
}

type fixture struct {
	store  bench.Store
	scopes *scopes
	m      Manager
	token  string
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gitcontrol.Command(context.Background(), dir, args...)
	cmd.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// newFixture is a ready v2 workspace held by "worker", as workspaceops'
// fixture builds it, with a recording Scopes.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	spec := workspace.Spec{Key: "card", CheckpointRepository: "api"}
	for _, name := range []string{"api", "web"} {
		repo := filepath.Join(root, name)
		if err := os.Mkdir(repo, 0700); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "init", "-b", "main")
		if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte(name+" base\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", "base")
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: name, Repository: repo})
	}
	ctx := context.Background()
	if _, err := store.InitializeWorkspaceContext(ctx, "creation", spec); err != nil {
		t.Fatal(err)
	}
	held, err := store.AcquireWorkspaceContext(ctx, "card", "worker")
	if err != nil {
		t.Fatal(err)
	}
	s := &scopes{}
	m, err := ForStore(store, s)
	if err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC) }
	return &fixture{store: store, scopes: s, m: m, token: held.Lease.Token}
}

// record is the private record, token included, as the state holds it.
func (f *fixture) record(t *testing.T) *model.Bench {
	t.Helper()
	var b *model.Bench
	if err := f.m.Prep.Read(func(rows benchstate.Database) error { b = workspace.Clone(rows["card"]); return nil }); err != nil || b == nil {
		t.Fatal("read", err)
	}
	return b
}

// change edits the record through the state's own update and validation.
func (f *fixture) change(t *testing.T, fn func(*model.Bench)) {
	t.Helper()
	err := f.m.Prep.Update(func(rows benchstate.Database, _ *sql.Tx) error {
		fn(rows["card"])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) grant(t *testing.T) Grant {
	t.Helper()
	g, err := f.m.Grant(context.Background(), "card", f.token)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

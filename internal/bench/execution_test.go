package bench

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

type testExecutor struct {
	mu           sync.Mutex
	active       bool
	startFailure bool
	fenceFailure bool
	observedEnv  []string
}

func (e *testExecutor) Create(string, string) (executionIdentity, error) {
	return executionIdentity{BootID: "test-boot", HostID: "test-host"}, nil
}
func (e *testExecutor) Start(c *exec.Cmd, _ *Execution) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.startFailure {
		return errors.New("injected start failure")
	}
	e.active = true
	e.observedEnv = c.Env
	return c.Start()
}
func (e *testExecutor) Fence(_ *Execution) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fenceFailure {
		return errors.New("cannot fence")
	}
	e.active = false
	return nil
}
func (e *testExecutor) Empty(_ *Execution) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active {
		return errors.New("scope populated")
	}
	return nil
}
func TestSupervisedRunCheckpointsAfterFencing(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	fake := &testExecutor{}
	s.executor = fake
	t.Setenv("BENCH_LEASE_TOKEN", "must-not-reach-command")
	result, e := s.Run(context.Background(), "task", "writer", []string{"sh", "-c", "printf hello"}, true)
	if e != nil {
		t.Fatal(e)
	}
	if result.ExitCode != 0 || result.Bench.Lease != nil {
		t.Fatal("successful run not released")
	}
	raw, e := os.ReadFile(result.Bench.Path + "/file")
	if e != nil || string(raw) != "base" {
		t.Fatal("changed work unexpectedly", e)
	}
	for _, v := range fake.observedEnv {
		if strings.HasPrefix(v, "BENCH_LEASE_TOKEN=") {
			t.Fatal("leaked lease token")
		}
	}
	states, e := s.Status("task")
	if e != nil || states[0].Checkpoint == "" || states[0].Checkpoint != result.Bench.Checkpoint || states[0].Lease != nil {
		t.Fatal("missing persisted checkpoint", e)
	}
}
func TestSupervisedFailureRetainsStoppedLease(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	s.executor = &testExecutor{}
	result, e := s.Run(context.Background(), "task", "writer", []string{"sh", "-c", "exit 7"}, true)
	if e == nil || result.ExitCode != 7 {
		t.Fatal(result, e)
	}
	states, e := s.Status("task")
	if e != nil || states[0].Lease == nil || states[0].Lease.Execution.Phase != "stopped" {
		t.Fatal("failed command lost lease", e)
	}
	if _, e = s.Lease("task", "other"); e == nil {
		t.Fatal("reassigned failed command's lease")
	}
}
func TestUnprovenFenceBlocksRelease(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	fake := &testExecutor{fenceFailure: true}
	s.executor = fake
	result, e := s.Run(context.Background(), "task", "writer", []string{"sh", "-c", "exit 0"}, true)
	if e == nil {
		t.Fatal("accepted unproven fence")
	}
	token := result.Bench.Lease.Token
	if _, e = s.Checkpoint("task", token); e == nil {
		t.Fatal("checkpoint while not fenced")
	}
	if e = s.Release("task", token); e == nil {
		t.Fatal("released unfenced execution")
	}
	if _, e = s.Stop("task", "wrong", false); e == nil {
		t.Fatal("unauthorized stop")
	}
	fake.fenceFailure = false
	if _, e = s.Stop("task", "", true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Checkpoint("task", token); e != nil {
		t.Fatal(e)
	}
	if e = s.Release("task", token); e != nil {
		t.Fatal(e)
	}
}
func TestStartFailureRetainsIntent(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	s.executor = &testExecutor{startFailure: true}
	result, e := s.Run(context.Background(), "task", "writer", []string{"sh", "-c", "exit 0"}, true)
	if e == nil || result == nil {
		t.Fatal("missing retained execution intent")
	}
	states, e := s.Status("task")
	if e != nil || states[0].Lease.Execution.Phase != "starting" {
		t.Fatal("lost start intent", e)
	}
	if _, e = s.Stop("task", result.Bench.Lease.Token, false); e != nil {
		t.Fatal(e)
	}
}

func TestStoppedIntentCannotLaunchLateWriter(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.Create("task", Options{Repo: r}, false); e != nil {
		t.Fatal(e)
	}
	fake := &testExecutor{}
	s.executor = fake
	b, e := s.Lease("task", "writer")
	if e != nil {
		t.Fatal(e)
	}
	execution := &Execution{ID: "delayed", Phase: "starting"}
	b.Lease.Execution = execution
	if e = s.transaction(func(db database) error { db["task"].Lease.Execution = execution; return nil }); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Stop("task", b.Lease.Token, false); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command("sh", "-c", "exit 0")
	if e = s.startExecution(context.Background(), "task", b, cmd, execution, func(start func() error) error { return start() }); e == nil {
		t.Fatal("delayed writer launched after stop")
	}
	if cmd.Process != nil {
		_ = cmd.Wait()
		t.Fatal("command executed after stop")
	}
}

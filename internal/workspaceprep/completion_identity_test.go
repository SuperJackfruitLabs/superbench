package workspaceprep

import (
	"database/sql"
	"errors"
	"os/exec"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

type completionBackend struct {
	fences   int
	fence    func()
	emptyErr error
}

func (*completionBackend) Create(string, string) (execution.Identity, error) {
	return execution.Identity{}, nil
}
func (*completionBackend) Start(*exec.Cmd, *execution.Execution) error { return nil }
func (b *completionBackend) Fence(*execution.Execution) error {
	b.fences++
	if b.fence != nil {
		b.fence()
	}
	return nil
}
func (b *completionBackend) Empty(*execution.Execution) error { return b.emptyErr }

func TestRunnerCompletionRequiresExactScopeAndCurrentOwner(t *testing.T) {
	changes := map[string]func(*model.Bench){
		"ID":      func(b *model.Bench) { b.Lease.Execution.ID = "different" },
		"root":    func(b *model.Bench) { b.Lease.Execution.Root += "changed" },
		"host":    func(b *model.Bench) { b.Lease.Execution.HostID += "changed" },
		"boot":    func(b *model.Bench) { b.Lease.Execution.BootID += "changed" },
		"log":     func(b *model.Bench) { b.Lease.Execution.Log += "changed" },
		"limit":   func(b *model.Bench) { b.Lease.Execution.LogLimitBytes++ },
		"owner":   func(b *model.Bench) { b.Lease.Token = "different" },
		"missing": func(b *model.Bench) { b.Lease.Execution = nil },
	}
	for name, change := range changes {
		for _, duringFence := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "-before", true: "-during-fence"}[duringFence], func(t *testing.T) {
				expected := &execution.Execution{ID: "run", Root: "/scope", HostID: "host", BootID: "boot", Log: "/state/run.log", LogLimitBytes: 4096, Phase: "starting"}
				current := *expected
				b := &model.Bench{Workspace: &model.Workspace{}, Lease: &model.Lease{Token: "owner", Execution: &current, StopRequested: true}}
				rows := benchstate.Database{"card": b}
				backend := &completionBackend{}
				m := Manager{Backend: backend, Gate: func(_ string, fn func() error) error { return fn() }, Read: func(fn func(benchstate.Database) error) error { return fn(rows) }, Update: func(fn workspace.Change) error { return fn(rows, (*sql.Tx)(nil)) }}
				if duringFence {
					backend.fence = func() { change(b) }
				} else {
					change(b)
				}
				out, err := m.completeExecution("card", "owner", expected)
				if err == nil || out != nil {
					t.Fatal("stale runner acknowledged different execution")
				}
				if !duringFence && backend.fences != 0 {
					t.Fatal("stale runner fenced a scope")
				}
				if b.Lease.Continuation != "" || !b.Lease.StopRequested {
					t.Fatal("completion revived continuation")
				}
			})
		}
	}
}

func TestRunnerCompletionRequiresIndependentEmptyProof(t *testing.T) {
	e := &execution.Execution{ID: "run", Root: "/scope", HostID: "host", BootID: "boot", Log: "/state/run.log", LogLimitBytes: 4096, Phase: "running"}
	b := &model.Bench{Workspace: &model.Workspace{}, Lease: &model.Lease{Token: "owner", Execution: e, StopRequested: true}}
	rows := benchstate.Database{"card": b}
	backend := &completionBackend{emptyErr: errors.New("still populated")}
	m := Manager{Backend: backend, Gate: func(_ string, fn func() error) error { return fn() }, Read: func(fn func(benchstate.Database) error) error { return fn(rows) }, Update: func(fn workspace.Change) error { return fn(rows, nil) }}
	if out, err := m.completeExecution("card", "owner", e); err == nil || out != nil || e.Phase != "running" {
		t.Fatal("completion accepted a populated scope")
	}
}

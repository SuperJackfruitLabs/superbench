package workspacerenew_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

// This backend runs exact simple children in their own process group. It gives
// no cgroup isolation; the Linux drills cover that.
type backend struct {
	mu        sync.Mutex
	processes map[string]*os.Process
	onStart   func()
	fences    int
}

func (b *backend) Create(string, string) (execution.Identity, error) {
	return execution.Identity{HostID: "fixture-host", BootID: "fixture-boot"}, nil
}
func (b *backend) Start(c *exec.Cmd, e *execution.Execution) error {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return err
	}
	b.mu.Lock()
	b.processes[e.ID] = c.Process
	hook := b.onStart
	b.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}
func (b *backend) Fence(e *execution.Execution) error {
	b.mu.Lock()
	b.fences++
	p := b.processes[e.ID]
	b.mu.Unlock()
	if p != nil {
		if group, err := syscall.Getpgid(p.Pid); err == nil && group == p.Pid {
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
	}
	return nil
}
func (*backend) Empty(*execution.Execution) error { return nil }

type fixture struct {
	t       *testing.T
	store   bench.Store
	manager workspaceprep.Manager
	backend *backend
	token   string
	gate    sync.Mutex
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := gitcontrol.Command(context.Background(), dir, args...)
	c.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", args[0], err, out)
	}
}

// newFixture returns a ready one-member workspace held by "writer".
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, backend: &backend{processes: map[string]*os.Process{}}}
	f.store = bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data"), CgroupRoot: filepath.Join(root, "no-cgroups")}
	repo := filepath.Join(root, "r00")
	if err := os.MkdirAll(filepath.Join(repo, ".bench"), 0700); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, ".bench", "config.toml"), []byte("[setup]\nonce = 'true'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fixture")
	spec := workspace.Spec{Key: "card", CheckpointRepository: "r00", Repositories: []workspace.Repository{{Name: "r00", Repository: repo}}}
	initial, err := f.store.StageWorkspaceContext(context.Background(), "create-1", spec)
	if err != nil {
		t.Fatal(err)
	}
	f.manager = workspaceprep.Manager{State: f.store.State, Data: f.store.Data, CgroupRoot: f.store.CgroupRoot, Backend: f.backend, Update: f.update, Read: func(fn func(benchstate.Database) error) error {
		return statestore.Read(f.store.State, func(db *sql.DB) error {
			rows, err := benchstate.Load(db)
			if err != nil {
				return err
			}
			return fn(rows)
		})
	}, Gate: func(_ string, fn func() error) error { f.gate.Lock(); defer f.gate.Unlock(); return fn() }, Git: func(ctx context.Context, dir string, args ...string) (string, error) {
		c := gitcontrol.Command(ctx, dir, args...)
		c.Env = gitcontrol.Environment(os.Environ())
		raw, err := c.Output()
		return strings.TrimSpace(string(raw)), err
	}, Admit: func() error { return nil }, Quota: func(*model.Bench) error { return nil }, ReserveLog: func(benchstate.Database) (uint64, error) { return 4096, nil }}
	f.manager.Seal = func(id string, c *output.Capture, exit *int, truncated bool) error {
		entry, err := retention.Snapshot(f.store.State, id)
		if err != nil {
			return err
		}
		info, err := c.File.Stat()
		if err != nil {
			return err
		}
		return f.update(func(_ benchstate.Database, tx *sql.Tx) error {
			return history.Seal(tx, id, entry, info.Size(), exit, truncated)
		})
	}
	if _, err := f.manager.Prepare(context.Background(), "card", initial.Lease.Token, "", false); err != nil {
		t.Fatal(err)
	}
	l := f.lifecycle()
	if _, err := l.Checkpoint(context.Background(), "card", initial.Lease.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := l.FinishInitialization(context.Background(), "card", initial.Lease.Token); err != nil {
		t.Fatal(err)
	}
	acquired, err := l.Acquire(context.Background(), "card", "writer")
	if err != nil {
		t.Fatal(err)
	}
	f.token = acquired.Lease.Token
	return f
}
func (f *fixture) update(change func(benchstate.Database, *sql.Tx) error) error {
	return f.within(func(update workspace.Update) error { return update(change) })
}
func (f *fixture) within(fn func(workspace.Update) error) error {
	return statestore.Within(f.store.State, nil, func(session *statestore.Session) error {
		return fn(func(change workspace.Change) error {
			return session.Update(func(tx *sql.Tx) error {
				if err := benchstate.Migrate(tx, f.store.State, func(*model.Bench) error { return nil }); err != nil {
					return err
				}
				rows, err := benchstate.Load(tx)
				if err != nil {
					return err
				}
				before := benchstate.Capture(rows)
				if err := change(rows, tx); err != nil {
					return err
				}
				if err := benchstate.Validate(rows); err != nil {
					return err
				}
				if err := benchstate.ValidateTransition(before, rows); err != nil {
					return err
				}
				for key, b := range rows {
					raw, err := json.Marshal(b)
					if err != nil {
						return err
					}
					if _, err := tx.Exec("INSERT INTO benches(key,record) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET record=excluded.record WHERE record!=excluded.record", key, string(raw)); err != nil {
						return err
					}
				}
				return nil
			})
		})
	})
}
func (f *fixture) read() *model.Bench {
	f.t.Helper()
	db, err := statestore.Open(filepath.Join(f.store.State, "bench.db"), true)
	if err != nil {
		f.t.Fatal(err)
	}
	defer db.Close()
	rows, err := benchstate.Load(db)
	if err != nil {
		f.t.Fatal(err)
	}
	return workspace.Clone(rows["card"])
}
func (f *fixture) lifecycle() workspace.Lifecycle {
	return workspace.Lifecycle{State: f.store.State, Data: f.store.Data, Git: f.manager.Git, Within: f.within, Prepared: workspaceprep.CheckPrepared, Released: workspaceprep.ReleaseHistory, Admit: func() error { return nil }, Allocate: func(*model.Bench) error { return nil }, Stopped: func(b *model.Bench) error {
		if b.Lease != nil && b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped" {
			return errors.New("test scope still active")
		}
		return nil
	}}
}
func (f *fixture) run(token string, args ...string) (*workspaceprep.RunResult, error) {
	return f.manager.Run(context.Background(), "card", token, "", args, false, nil)
}

// stopped leaves the writer administratively stopped after a payload that
// exited zero but left an untracked file at the tree root.
func (f *fixture) stopped() *model.Bench {
	f.t.Helper()
	if out, err := f.run(f.token, "/bin/sh", "-c", "echo stray > stray.txt"); err != nil || out.ExitCode != 0 {
		f.t.Fatal("stray payload", err)
	}
	if _, err := f.manager.Stop("card", "", true); err != nil {
		f.t.Fatal(err)
	}
	b := f.read()
	if !b.Lease.StopRequested || b.Lease.Continuation != "" || b.Lease.Execution == nil || b.Lease.Execution.Phase != "stopped" {
		f.t.Fatal("fixture lease is not administratively stopped")
	}
	return b
}

package workspacerepair_test

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceops"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
)

// This backend checks simple-child lifecycle ordering, not managed isolation.
type backend struct {
	mu        sync.Mutex
	processes map[string]*os.Process
	starts    int
	fenceErr  error
	onStart   func()
}

func (*backend) Create(string, string) (execution.Identity, error) {
	return execution.Identity{HostID: "fixture-host", BootID: "fixture-boot"}, nil
}
func (b *backend) Start(c *exec.Cmd, e *execution.Execution) error {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return err
	}
	b.mu.Lock()
	b.processes[e.ID] = c.Process
	b.starts++
	hook := b.onStart
	b.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}
func (b *backend) Fence(e *execution.Execution) error {
	b.mu.Lock()
	p, err := b.processes[e.ID], b.fenceErr
	b.mu.Unlock()
	if err != nil {
		return err
	}
	if p != nil {
		if group, err := syscall.Getpgid(p.Pid); err == nil && group == p.Pid {
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
	}
	return nil
}
func (*backend) Empty(*execution.Execution) error { return nil }
func (b *backend) count() int                     { b.mu.Lock(); defer b.mu.Unlock(); return b.starts }

type fixture struct {
	t       *testing.T
	store   bench.Store
	m       workspacerepair.Manager
	backend *backend
	spec    workspace.Spec
	initial *model.Bench
	request protocolv2.Request
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := gitcontrol.Command(context.Background(), dir, args...)
	c.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func newFixture(t *testing.T, hooks func(int) string) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	f := &fixture{t: t, backend: &backend{processes: map[string]*os.Process{}}}
	f.store = bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data"), CgroupRoot: filepath.Join(root, "fixture-cgroups")}
	f.spec = workspace.Spec{Key: "card", CheckpointRepository: "a"}
	for i, name := range []string{"a", "b"} {
		repo := filepath.Join(root, "source-"+name)
		must(t, os.MkdirAll(filepath.Join(repo, ".bench"), 0700))
		git(t, repo, "init", "-b", "main")
		must(t, os.WriteFile(filepath.Join(repo, "source.txt"), []byte(name), 0600))
		must(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".once-output\n.each-output\n"), 0600))
		if hooks != nil {
			must(t, os.WriteFile(filepath.Join(repo, ".bench", "config.toml"), []byte(hooks(i)), 0600))
		}
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", "fixture")
		f.spec.Repositories = append(f.spec.Repositories, workspace.Repository{Name: name, Repository: repo})
	}
	yes := true
	f.request = protocolv2.Request{Version: protocolv2.Version, Operation: "create", Key: "card", RequestID: "fixture-create", Initialize: &yes, CheckpointRepository: f.spec.CheckpointRepository}
	for _, repo := range f.spec.Repositories {
		f.request.Repositories = append(f.request.Repositories, protocolv2.Repository{Name: repo.Name, Repository: repo.Repository})
	}
	id, err := workspaceops.CreationID(f.request, 42)
	must(t, err)
	f.initial, err = f.store.StageWorkspaceContext(context.Background(), id, f.spec)
	must(t, err)
	f.m, err = f.store.WorkspaceRepair()
	must(t, err)
	f.m.Prep.Backend = f.backend
	return f
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) row() *model.Bench {
	f.t.Helper()
	var out *model.Bench
	must(f.t, f.m.Prep.Read(func(rows benchstate.Database) error { out = workspace.Clone(rows["card"]); return nil }))
	return out
}
func (f *fixture) stop() {
	f.t.Helper()
	_, err := f.m.Prep.Stop("card", f.row().Lease.Token, false)
	must(f.t, err)
}
func (f *fixture) inspect(choices ...string) *workspacerepair.Plan {
	f.t.Helper()
	p, err := f.m.Inspect(context.Background(), "card", choices)
	must(f.t, err)
	return p
}
func (f *fixture) claim(p *workspacerepair.Plan, id string) *workspacerepair.Result {
	f.t.Helper()
	r, err := f.m.Claim(context.Background(), *p, id)
	must(f.t, err)
	if r == nil || r.GrantToken == "" {
		f.t.Fatal("repair did not grant its new owner")
	}
	return r
}
func (f *fixture) update(fn func(benchstate.Database, *sql.Tx) error) error {
	return f.m.Prep.Update(fn)
}
func (f *fixture) sql(fn func(*sql.Tx) error) error {
	db, err := statestore.Open(filepath.Join(f.store.State, "bench.db"), false)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func (f *fixture) stateBytes() []byte {
	f.t.Helper()
	db, err := statestore.Open(filepath.Join(f.store.State, "bench.db"), true)
	must(f.t, err)
	defer db.Close()
	type entry struct{ Table, Key, Value string }
	var all []entry
	for _, table := range []string{"benches", "run_history", "setup_receipts", "initialization_repairs"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY 1,2")
		must(f.t, err)
		cols, err := rows.Columns()
		must(f.t, err)
		for rows.Next() {
			values := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range values {
				dest[i] = &values[i]
			}
			must(f.t, rows.Scan(dest...))
			raw, err := json.Marshal(values)
			must(f.t, err)
			all = append(all, entry{Table: table, Value: string(raw)})
		}
		must(f.t, rows.Err())
		rows.Close()
	}
	raw, err := json.Marshal(all)
	must(f.t, err)
	return raw
}

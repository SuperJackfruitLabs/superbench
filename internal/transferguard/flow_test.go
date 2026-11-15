package transferguard_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/transferguard"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

// This backend exercises lifecycle/state ordering with exact simple children.
// It provides no cgroup or detached-descendant isolation; Linux drills do that.
type backend struct {
	mu              sync.Mutex
	processes       map[string]*os.Process
	creates, starts int
	onStart         func()
}

func (b *backend) Create(string, string) (execution.Identity, error) {
	b.mu.Lock()
	b.creates++
	b.mu.Unlock()
	return execution.Identity{HostID: "fixture-host", BootID: "fixture-boot"}, nil
}
func (b *backend) Start(c *exec.Cmd, e *execution.Execution) error {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return err
	}
	b.mu.Lock()
	b.starts++
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
func (b *backend) counts() (int, int)             { b.mu.Lock(); defer b.mu.Unlock(); return b.creates, b.starts }

type fixture struct {
	t         *testing.T
	store     bench.Store
	manager   workspaceprep.Manager
	backend   *backend
	initial   *model.Bench
	gate      sync.Mutex
	failProof bool
	afterSeal func()
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := gitcontrol.Command(context.Background(), dir, args...)
	c.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", args[0], err, out)
	}
	return strings.TrimSpace(string(out))
}
func newFixture(t *testing.T, n int, script func(int) string) *fixture {
	t.Helper()
	root, rootErr := filepath.EvalSymlinks(t.TempDir())
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	f := &fixture{t: t, backend: &backend{processes: map[string]*os.Process{}}}
	f.store = bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data"), CgroupRoot: filepath.Join(root, "no-cgroups")}
	spec := workspace.Spec{Key: "card", CheckpointRepository: "r00"}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("r%02d", i)
		repo := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(repo, ".bench"), 0700); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "init", "-b", "main")
		commands := "[setup]\nonce = 'echo once >> once-count'\neach = 'echo each >> each-count'\n"
		if script != nil {
			commands = script(i)
		}
		if err := os.WriteFile(filepath.Join(repo, ".bench", "config.toml"), []byte(commands), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", "fixture")
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: name, Repository: repo})
	}
	var err error
	f.initial, err = f.store.StageWorkspaceContext(context.Background(), "create-1", spec)
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
		err = f.update(func(_ benchstate.Database, tx *sql.Tx) error {
			return history.Seal(tx, id, entry, info.Size(), exit, truncated)
		})
		if err == nil && f.afterSeal != nil {
			f.afterSeal()
		}
		return err
	}
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
				proofs := len(rows["card"].Workspace.Preparation)
				if err := change(rows, tx); err != nil {
					return err
				}
				if f.failProof && len(rows["card"].Workspace.Preparation) > proofs {
					f.failProof = false
					return errors.New("injected proof save failure")
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
		if b.Lease != nil && b.Lease.Execution != nil {
			if b.Lease.Execution.Phase != "stopped" {
				return errors.New("test scope still active")
			}
			return f.backend.Empty(b.Lease.Execution)
		}
		return nil
	}}
}
func (f *fixture) ready() string {
	f.t.Helper()
	old := f.initial.Lease.Token
	if _, err := f.manager.Prepare(context.Background(), "card", f.initial.Lease.Token, "", false); err != nil {
		f.t.Fatal(err)
	}
	for _, member := range f.initial.Workspace.Members {
		git(f.t, member.Path, "add", ".")
		git(f.t, member.Path, "commit", "-m", "explicitly preserve preparation output")
	}
	lifecycle := f.lifecycle()
	if _, err := lifecycle.Checkpoint(context.Background(), "card", old); err != nil {
		f.t.Fatal(err)
	}
	if _, err := lifecycle.FinishInitialization(context.Background(), "card", old); err != nil {
		f.t.Fatal(err)
	}
	acquired, err := lifecycle.Acquire(context.Background(), "card", "writer")
	if err != nil {
		f.t.Fatal(err)
	}
	f.initial = acquired
	return old
}

func TestMissingSourceReturnStopsDestinationBeforeNextMember(t *testing.T) {
	f := newFixture(t, 2, func(i int) string {
		if i == 0 {
			return "[setup]\nonce = 'echo once >> once-count'\neach = 'echo each >> each-count; case \"$PWD\" in */target/*) mkdir -p ../../card/r01; echo preserved > ../../card/r01/sentinel;; esac'\n"
		}
		return "[setup]\nonce = 'echo once >> once-count'\neach = 'echo each >> each-count'\n"
	})
	f.ready()
	if _, err := f.lifecycle().Release(context.Background(), "card", f.initial.Lease.Token); err != nil {
		t.Fatal(err)
	}
	source := f.read()
	if err := os.RemoveAll(source.Workspace.Members[1].Path); err != nil {
		t.Fatal(err)
	}
	manager := workspacetransfer.Manager{Store: f.store}
	plan, err := manager.InspectMissing(context.Background(), "card", "target", "transfer-guard", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.BeginMissing(context.Background(), *plan); err != nil {
		t.Fatal(err)
	}
	// The host backend cannot run against this fixture's ordinary directory. It
	// still performs real source retirement and stages the exact destination.
	_, err = manager.ContinueMissing(context.Background(), "card", plan.ID)
	if err == nil {
		t.Fatal("ordinary directory unexpectedly supervised initialization")
	}
	var target *model.Bench
	readTarget := func() {
		t.Helper()
		if err := f.manager.Read(func(rows benchstate.Database) error { target = workspace.Clone(rows["target"]); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	readTarget()
	if target == nil || target.Lease == nil || target.Lease.StopRequested {
		t.Fatal("fixture did not preserve initial destination owner")
	}
	token := target.Lease.Token
	// A raw supplied-token setup cannot adopt the private incoming continuation.
	if _, err = f.manager.Prepare(context.Background(), "target", token, "", false); err == nil {
		t.Fatal("raw setup bypassed transfer authority")
	}

	// The guard accepts the real journal, then independently binds public target
	// metadata rather than accepting a caller's origin-shaped assertion.
	config := transferguard.Config{State: f.store.State, Data: f.store.Data, Git: f.manager.Git}
	check := func(candidate *model.Bench) error {
		return f.update(func(_ benchstate.Database, tx *sql.Tx) error {
			return transferguard.Check(context.Background(), config, tx, candidate)
		})
	}
	if err = check(target); err != nil {
		t.Fatal("valid retained source refused", err)
	}
	for name, mutate := range map[string]func(*model.Bench){
		"origin":   func(b *model.Bench) { b.Workspace.TransferredFrom.SourceID = strings.Repeat("f", 32) },
		"creation": func(b *model.Bench) { b.Workspace.CreationID = "other" },
		"root":     func(b *model.Bench) { b.Path = filepath.Join(f.store.Data, "elsewhere") },
		"seal":     func(b *model.Bench) { b.Workspace.RecoveredFrom.Commit = strings.Repeat("f", 40) },
		"quota":    func(b *model.Bench) { b.RequestedQuotaBytes++ },
		"member":   func(b *model.Bench) { b.Workspace.Members[1].Branch = "bench/other" },
	} {
		candidate := workspace.Clone(target)
		mutate(candidate)
		if err = check(candidate); !errors.Is(err, transferguard.ErrGuard) {
			t.Fatalf("%s not bound: %v", name, err)
		}
	}
	presentArtifact := filepath.Join(source.Workspace.Members[0].Path, "private-return")
	if err = os.WriteFile(presentArtifact, []byte("owned evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = check(target); !errors.Is(err, transferguard.ErrGuard) {
		t.Fatal("changed present source was accepted", err)
	}
	if raw, e := os.ReadFile(presentArtifact); e != nil || string(raw) != "owned evidence" {
		t.Fatal("present evidence changed", e)
	}
	if err = os.Remove(presentArtifact); err != nil {
		t.Fatal(err)
	}
	f.manager.TransferID = plan.ID
	beforeCreates, beforeStarts := f.backend.counts()
	result, err := f.manager.Prepare(context.Background(), "target", token, "", false)
	if !errors.Is(err, transferguard.ErrGuard) || result == nil || result.Lease == nil || !result.Lease.StopRequested || result.Lease.Continuation != "" {
		t.Fatal("changed missing source did not synchronously revoke target", err)
	}
	if creates, starts := f.backend.counts(); creates != beforeCreates+1 || starts != beforeStarts+1 {
		t.Fatal("later member acquired or launched a scope")
	}
	sentinel := filepath.Join(source.Workspace.Members[1].Path, "sentinel")
	raw, err := os.ReadFile(sentinel)
	if err != nil || string(raw) != "preserved\n" {
		t.Fatal("source reappearance was changed", err)
	}
	readTarget()
	if target.Workspace.Stage == nil || target.Workspace.Stage.Name != "r00" || len(target.Workspace.Preparation) != 0 || target.Lease.Execution == nil {
		t.Fatal("guard fabricated success or discarded held history")
	}
	// A's actual sealed zero outcome remains salvageable, without inventing B.
	if err = f.update(func(_ benchstate.Database, tx *sql.Tx) error {
		h, e := history.Get(tx, target.Lease.Execution.ID)
		if e != nil {
			return e
		}
		if h.File == nil || h.ExitCode == nil || *h.ExitCode != 0 || !h.ReleasedAt.IsZero() {
			return errors.New("known A outcome lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Explicit repair authority does not override a returned source checkout.
	if err = f.update(func(_ benchstate.Database, tx *sql.Tx) error {
		_, e := f.manager.InspectInitializationRepair(context.Background(), target, tx, nil)
		return e
	}); !errors.Is(err, transferguard.ErrGuard) {
		t.Fatal("explicit repair bypassed retained-source proof", err)
	}
	if creates, starts := f.backend.counts(); creates != beforeCreates+1 || starts != beforeStarts+1 {
		t.Fatal("repair launched despite returned source")
	}

	// Preserve the returned checkout as owned evidence outside the source path.
	// Restoring approved absence permits inspection, never ordinary revival.
	evidence := filepath.Join(filepath.Dir(f.store.State), "returned-source-evidence")
	if err = os.Rename(source.Workspace.Members[1].Path, evidence); err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.Prepare(context.Background(), "target", token, "", false); err == nil {
		t.Fatal("ordinary continuation revived stopped target")
	}
	if err = f.update(func(_ benchstate.Database, tx *sql.Tx) error {
		decisions, e := f.manager.InspectInitializationRepair(context.Background(), target, tx, nil)
		if e != nil {
			return e
		}
		if len(decisions) != 2 || decisions[0].Name != "r00" || decisions[0].Action != "salvage" || decisions[0].RunID != target.Lease.Execution.ID || decisions[1].Name != "r01" || decisions[1].Action != "prepare" {
			return errors.New("repair lost genuine A outcome or invented B completion")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if raw, e := os.ReadFile(filepath.Join(evidence, "sentinel")); e != nil || string(raw) != "preserved\n" {
		t.Fatal("repair inspection lost returned evidence", e)
	}
}

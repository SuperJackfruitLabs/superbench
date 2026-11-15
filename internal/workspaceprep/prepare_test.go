package workspaceprep_test

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
	"time"

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
	root := t.TempDir()
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
func (f *fixture) prepare(ctx context.Context, selector string) (*model.Bench, error) {
	return f.manager.Prepare(ctx, "card", f.initial.Lease.Token, selector, false)
}
func (f *fixture) count(member int, file string) int {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.initial.Workspace.Members[member].Path, file))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return len(strings.Fields(string(raw)))
}

func TestWholeWorkspaceHooksPrepareAndRetainAllEvidence(t *testing.T) {
	f := newFixture(t, 2, nil)
	b, err := f.prepare(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Workspace.Phase != "prepared" || len(b.Workspace.Preparation) != 2 || len(b.Lease.RetainedRuns) != 2 || b.Lease.Execution != nil || b.Workspace.Stage != nil {
		t.Fatalf("incomplete preparation: %+v", b)
	}
	if b.Lease.Token != f.initial.Lease.Token || b.Lease.Continuation != f.initial.Lease.Continuation {
		t.Fatal("whole workspace ownership replaced")
	}
	for i := 0; i < 2; i++ {
		if f.count(i, "once-count") != 1 || f.count(i, "each-count") != 1 {
			t.Fatal("member hooks not run exactly once")
		}
	}
	if err := workspaceprep.CheckPrepared(b); err != nil {
		t.Fatal(err)
	}
	if _, err := f.prepare(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, starts := f.backend.counts(); starts != 2 {
		t.Fatal("complete preparation repeated hooks")
	}
}

func TestEarlierMemberProofSurvivesLaterHookFailure(t *testing.T) {
	f := newFixture(t, 2, func(i int) string {
		if i == 1 {
			return "[setup]\nonce = 'echo once >> once-count'\neach = 'test -f allow; echo each >> each-count'\n"
		}
		return "[setup]\nonce = 'echo once >> once-count'\neach = 'echo each >> each-count'\n"
	})
	b, err := f.prepare(context.Background(), "")
	if err == nil || len(b.Workspace.Preparation) != 1 || b.Lease == nil || b.Lease.Execution == nil || b.Lease.Execution.Phase != "stopped" {
		t.Fatalf("later failure lost prior proof/ownership: %+v %v", b, err)
	}
	if err := os.WriteFile(filepath.Join(b.Workspace.Members[1].Path, "allow"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	b, err = f.prepare(context.Background(), "")
	if err != nil || b.Workspace.Phase != "prepared" {
		t.Fatalf("retry: %+v %v", b, err)
	}
	if f.count(0, "once-count") != 1 || f.count(0, "each-count") != 1 {
		t.Fatal("earlier successful member repeated")
	}
	if _, starts := f.backend.counts(); starts != 3 {
		t.Fatalf("wrong stage count: %d", starts)
	}
}

func TestSealedSuccessProofSaveRetryDoesNotRepeatHooks(t *testing.T) {
	f := newFixture(t, 2, nil)
	f.failProof = true
	b, err := f.prepare(context.Background(), "")
	if err == nil || b == nil || b.Lease.Execution == nil || b.Lease.Execution.Phase != "stopped" || len(b.Workspace.Preparation) != 0 {
		t.Fatalf("proof failure not retained: %+v %v", b, err)
	}
	if f.count(0, "once-count") != 1 {
		t.Fatal("first successful hook missing")
	}
	b, err = f.prepare(context.Background(), "")
	if err != nil || b.Workspace.Phase != "prepared" {
		t.Fatalf("retry sealed proof: %+v %v", b, err)
	}
	if f.count(0, "once-count") != 1 || f.count(0, "each-count") != 1 {
		t.Fatal("sealed successful stage was executed again")
	}
	if _, starts := f.backend.counts(); starts != 2 {
		t.Fatalf("sealed stage repeated: %d", starts)
	}
}

func TestChangedInputsRevokePreparedProofBeforeEffects(t *testing.T) {
	for _, name := range []string{"package.json", ".bench/config.toml"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 2, nil)
			if _, err := f.prepare(context.Background(), ""); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.initial.Workspace.Members[0].Path, name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, []byte("\n")...)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := workspaceprep.CheckPrepared(f.read()); err == nil {
				t.Fatal("changed inputs accepted as prepared")
			}
			blocked := errors.New("stop before new effects")
			f.manager.Admit = func() error {
				b := f.read()
				if b.Workspace.Phase != "initializing" || len(b.Workspace.Preparation) != 1 || b.Workspace.Preparation[0].Name != "r01" {
					t.Error("proof invalidation was not committed before admission")
				}
				return blocked
			}
			_, err = f.prepare(context.Background(), "r00")
			if !errors.Is(err, blocked) {
				t.Fatalf("expected effect gate: %v", err)
			}
			if f.count(0, "once-count") != 1 || f.count(0, "each-count") != 1 {
				t.Fatal("hook executed before revoked proof committed")
			}
			f.manager.Admit = func() error { return nil }
			if _, err := f.prepare(context.Background(), "r00"); err != nil {
				t.Fatal(err)
			}
			if f.count(0, "once-count") != 2 || f.count(0, "each-count") != 2 {
				t.Fatal("changed preparation inputs reused stale once receipt")
			}
		})
	}
}

func TestOperatorStopPreventsLaterMemberAndContinuation(t *testing.T) {
	f := newFixture(t, 2, nil)
	f.afterSeal = func() {
		f.afterSeal = nil
		if _, err := f.manager.Stop("card", f.initial.Lease.Token, false); err != nil {
			t.Error(err)
		}
	}
	b, err := f.prepare(context.Background(), "")
	if err == nil || b == nil || !b.Lease.StopRequested || b.Lease.Continuation != "" {
		t.Fatalf("operator stop did not revoke continuation: %+v %v", b, err)
	}
	if f.count(1, "once-count") != 0 {
		t.Fatal("later member started after stop")
	}
	if _, err := f.prepare(context.Background(), ""); err == nil {
		t.Fatal("stopped initialization continued")
	}
}

func TestWrongTokenHasNoPreparationEffects(t *testing.T) {
	f := newFixture(t, 2, nil)
	before, err := os.ReadFile(filepath.Join(f.store.State, "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", strings.Repeat("0", 48)} {
		if b, err := f.manager.Prepare(context.Background(), "card", token, "", false); err == nil || b != nil {
			t.Fatal("wrong owner accepted or disclosed the current owner's capabilities", err)
		}
	}
	after, err := os.ReadFile(filepath.Join(f.store.State, "bench.db"))
	if err != nil || string(before) != string(after) {
		t.Fatal("wrong-token request changed authoritative state")
	}
	if creates, starts := f.backend.counts(); creates != 0 || starts != 0 {
		t.Fatal("wrong-token request created execution scope")
	}
	if f.count(0, "once-count") != 0 || f.count(1, "once-count") != 0 {
		t.Fatal("wrong-token request ran hooks")
	}
}

func TestThirtyTwoMembersPrepareBeyondScalarRetentionCap(t *testing.T) {
	f := newFixture(t, 32, nil)
	b, err := f.prepare(context.Background(), "")
	if err != nil || b == nil || b.Workspace.Phase != "prepared" || len(b.Workspace.Preparation) != 32 || len(b.Lease.RetainedRuns) != 32 {
		t.Fatalf("32 member preparation failed: %v", err)
	}
	if _, starts := f.backend.counts(); starts != 32 {
		t.Fatalf("stage count: %d", starts)
	}
}

func TestCancellationRevokesContinuationAndDoesNotStartLaterMember(t *testing.T) {
	f := newFixture(t, 2, func(i int) string {
		if i == 0 {
			return "[setup]\nonce = 'exec sleep 30'\n"
		}
		return "[setup]\nonce = 'echo once >> once-count'\n"
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	f.backend.onStart = func() { close(started) }
	done := make(chan error, 1)
	go func() { _, err := f.prepare(ctx, ""); done <- err }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("payload did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled preparation succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled preparation did not stop")
	}
	b := f.read()
	if !b.Lease.StopRequested || b.Lease.Continuation != "" {
		t.Fatal("canceled preparation returned before revoking initialization continuation")
	}
	if f.count(1, "once-count") != 0 {
		t.Fatal("later member ran after cancel")
	}
	if _, err := f.prepare(context.Background(), ""); err == nil {
		t.Fatal("immediate retry continued canceled initialization")
	}
}

func TestEarlierHookCannotRedirectLaterMemberWithIdenticalInputs(t *testing.T) {
	outside := t.TempDir()
	// The later checkout uses private caches, so following its replacement would
	// both run hooks and create cache directories outside the registered root.
	secondConfig := "[setup]\nonce = 'echo once >> once-count'\neach = 'echo each >> each-count'\n[caches]\nnpm = false\n"
	// Shell path is a fixture-generated path, quoted for literal interpretation.
	literalOutside := "'" + strings.ReplaceAll(outside, "'", "'\\''") + "'"
	swap := "mv ../r01 ../preserved-r01; ln -s " + literalOutside + " ../r01; echo once >> once-count"
	f := newFixture(t, 2, func(i int) string {
		if i == 0 {
			return "[setup]\nonce = " + fmt.Sprintf("%q", swap) + "\n"
		}
		return secondConfig
	})
	if err := os.Mkdir(filepath.Join(outside, ".bench"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{".bench/config.toml": secondConfig, "package.json": "{}\n"} {
		if err := os.WriteFile(filepath.Join(outside, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// An unrelated real repository makes ls-files succeed too: matching config
	// and input bytes alone must never authorize this checkout identity.
	git(t, outside, "init", "-b", "main")
	git(t, outside, "add", ".")
	git(t, outside, "commit", "-m", "unrelated checkout")
	b, err := f.prepare(context.Background(), "")
	if err == nil || b == nil || b.Lease == nil || b.Lease.Token != f.initial.Lease.Token {
		t.Fatalf("redirected later member was not refused with ownership held: %+v %v", b, err)
	}
	if creates, starts := f.backend.counts(); creates != 1 || starts != 1 {
		t.Fatalf("later redirected member obtained execution scope: %d/%d", creates, starts)
	}
	for _, name := range []string{"once-count", "each-count", ".bench/cache"} {
		if _, err := os.Lstat(filepath.Join(outside, name)); !os.IsNotExist(err) {
			t.Fatalf("preparation affected redirected directory %s: %v", name, err)
		}
	}
	if target, err := os.Readlink(f.initial.Workspace.Members[1].Path); err != nil || target != outside {
		t.Fatal("refusal modified replacement or lost evidence")
	}
	if _, err := os.Lstat(filepath.Join(b.Path, "preserved-r01", ".git")); err != nil {
		t.Fatal("original later checkout was not preserved")
	}
}

func TestStopBeforeDelayedLaunchPreventsAnyPayloadStart(t *testing.T) {
	f := newFixture(t, 2, nil)
	base := f.manager.Gate
	calls := 0
	f.manager.Gate = func(key string, fn func() error) error {
		calls++
		if calls == 2 {
			// Acquisition committed its starting intent, but the delayed supervisor
			// has not entered its launch gate. An operator stop wins this ordering.
			if _, err := f.manager.Stop(key, f.initial.Lease.Token, false); err != nil {
				return err
			}
		}
		return base(key, fn)
	}
	b, err := f.prepare(context.Background(), "")
	if err == nil || b == nil || b.Lease == nil || !b.Lease.StopRequested || b.Lease.Continuation != "" {
		t.Fatalf("delayed launch ignored stop: %+v %v", b, err)
	}
	if creates, starts := f.backend.counts(); creates != 1 || starts != 0 {
		t.Fatalf("payload launched after its scope was stopped: %d/%d", creates, starts)
	}
	if len(b.Workspace.Preparation) != 0 || f.count(0, "once-count") != 0 || f.count(1, "once-count") != 0 {
		t.Fatal("stopped delayed stage produced preparation evidence/effects")
	}
}

func TestRetainedPreparationRequiresSealedSuccessfulHistory(t *testing.T) {
	for _, damage := range []string{"missing", "failed"} {
		t.Run(damage, func(t *testing.T) {
			f := newFixture(t, 2, nil)
			b, err := f.prepare(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			id := b.Workspace.Preparation[0].RunID
			if err := f.update(func(_ benchstate.Database, tx *sql.Tx) error {
				if damage == "missing" {
					_, err := tx.Exec("DELETE FROM run_history WHERE id=?", id)
					return err
				}
				record, err := history.Get(tx, id)
				if err != nil {
					return err
				}
				exit := 7
				record.ExitCode = &exit
				return history.Save(tx, record)
			}); err != nil {
				t.Fatal(err)
			}
			beforeCreates, beforeStarts := f.backend.counts()
			if _, err := f.prepare(context.Background(), "r00"); err == nil {
				t.Fatal("preparation reused missing or failed history as a successful proof")
			}
			if creates, starts := f.backend.counts(); creates != beforeCreates || starts != beforeStarts {
				t.Fatal("invalid retained evidence launched effects instead of refusing")
			}
			if f.count(0, "once-count") != 1 || f.count(0, "each-count") != 1 {
				t.Fatal("invalid retained proof silently repeated irreversible hooks")
			}
		})
	}
}

func TestHookPreparationCannotUseACommandFreeProof(t *testing.T) {
	f := newFixture(t, 2, nil)
	if _, err := f.prepare(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if err := f.update(func(rows benchstate.Database, _ *sql.Tx) error {
		rows["card"].Workspace.Preparation[0].RunID = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := workspaceprep.CheckPrepared(f.read()); err == nil {
		t.Fatal("hook configuration accepted a command-free preparation proof")
	}
}

func TestCanceledCachedPreparationReconcilesBeforeReturning(t *testing.T) {
	f := newFixture(t, 2, nil)
	if _, err := f.prepare(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	beforeCreates, beforeStarts := f.backend.counts()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	update := f.manager.Update
	calls := 0
	f.manager.Update = func(change func(benchstate.Database, *sql.Tx) error) error {
		calls++
		// Both current member proofs were cached; no execution watcher exists.
		// Cancel just before the final all-member phase update begins.
		if calls == 3 {
			cancel()
		}
		return update(change)
	}
	b, err := f.manager.Prepare(ctx, "card", f.initial.Lease.Token, "", false)
	if !errors.Is(err, context.Canceled) || b == nil || b.Lease == nil || !b.Lease.StopRequested || b.Lease.Continuation != "" {
		t.Fatalf("cached preparation returned before cancellation reconciliation: %+v %v", b, err)
	}
	if creates, starts := f.backend.counts(); creates != beforeCreates || starts != beforeStarts {
		t.Fatal("cached canceled request repeated hooks")
	}
	if _, err := f.prepare(context.Background(), ""); err == nil {
		t.Fatal("canceled cached preparation continued immediately")
	}
}

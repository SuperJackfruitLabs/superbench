package freshtree_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

var identity = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}

func git(dir string, args ...string) (string, error) {
	c := gitcontrol.Command(context.Background(), dir, args...)
	c.Env = append(gitcontrol.Environment(os.Environ()), identity...)
	out, err := c.Output()
	return string(out), err
}

func must(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

// payload stands in for the fixed helper: it alone may enter the private root,
// which stays mode 0000 to the daemon outside each of its observations.
type payload struct {
	t        *testing.T
	root     string
	mu       sync.Mutex
	calls    map[string]int
	exported int
}

func (p *payload) as(kind string, fn func() error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[kind]++
	if err := os.Chmod(p.root, 0700); err != nil {
		return err
	}
	defer func() {
		if err := os.Chmod(p.root, 0); err != nil {
			p.t.Error(err)
		}
	}()
	return fn()
}

func (p *payload) Root(_ context.Context, b *model.Bench, clean bool) error {
	return p.as("root", func() error {
		entries, err := os.ReadDir(b.Path)
		if err != nil {
			return err
		}
		known := map[string]bool{".superbench-git": true, ".superbench-runtime": true}
		for _, m := range b.Workspace.Members {
			known[m.Name] = true
		}
		for _, e := range entries {
			if clean && !known[e.Name()] {
				return model.ErrDirty
			}
		}
		return nil
	})
}

func (p *payload) Git(_ context.Context, b *model.Bench, member string, args ...string) (out string, err error) {
	err = p.as("git", func() error {
		out, err = git(filepath.Join(b.Path, member), args...)
		return err
	})
	return out, err
}

func inputFile(dir, name string, data bool) registeredinspection.InputFile {
	f := registeredinspection.InputFile{Name: name}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return f
	}
	sum := sha256.Sum256(raw)
	f.Present, f.Bytes, f.SHA256 = true, uint64(len(raw)), hex.EncodeToString(sum[:])
	if data {
		f.Data = raw
	}
	return f
}

func (p *payload) Inputs(_ context.Context, b *model.Bench, member string) (in registeredinspection.Inputs, err error) {
	err = p.as("inputs", func() error {
		dir := filepath.Join(b.Path, member)
		in = registeredinspection.Inputs{Version: registeredinspection.InputsVersion, Member: member, Config: inputFile(dir, ".bench/config.toml", true)}
		for _, name := range []string{"package.json", "pnpm-lock.yaml", "package-lock.json"} {
			in.Manifests = append(in.Manifests, inputFile(dir, name, false))
		}
		return nil
	})
	return in, err
}

func (p *payload) Transport(*model.Bench, string) (stageexec.PrepareTransport, error) {
	return nil, errors.New("this fixture has no setup stage")
}

func (p *payload) Export(_ context.Context, b *model.Bench, member string, exclude []string) (raw []byte, err error) {
	err = p.as("export", func() error {
		args := []string{"bundle", "create", "-", "HEAD"}
		if len(exclude) != 0 {
			args = append(append(args, "--not"), exclude...)
		}
		out, e := git(filepath.Join(b.Path, member), args...)
		raw = []byte(out)
		p.exported++
		return e
	})
	return raw, err
}

type fixture struct {
	t       *testing.T
	state   string
	data    string
	payload *payload
	staged  *model.Bench
	gate    sync.Mutex
	// verified counts read-only checks of the pinned Fresh allocation;
	// quotaErr, when set, is root's refusal of it.
	verified int
	quotaErr error
}

// errLegacyQuota is the helper's refusal of a legacy apply for an allocation
// that its retained bootstrap authority owns.
var errLegacyQuota = errors.New("registered payload allocation requires its retained bootstrap authority")

func (f *fixture) freshQuota(b *model.Bench) error {
	if b.Path != f.payload.root || b.ProjectID != 10000 || b.QuotaBytes != 1<<30 || b.QuotaInodes != 1000 {
		return errors.New("fresh quota verification saw another allocation")
	}
	f.verified++
	return f.quotaErr
}

func (f *fixture) within(fn func(workspace.Update) error) error {
	return statestore.Within(f.state, nil, func(s *statestore.Session) error {
		return fn(func(change workspace.Change) error {
			return s.Update(func(tx *sql.Tx) error {
				if err := benchstate.Migrate(tx, f.state, func(*model.Bench) error { return nil }); err != nil {
					return err
				}
				rows, err := benchstate.Load(tx)
				if err != nil {
					return err
				}
				before := benchstate.Capture(rows)
				if err = change(rows, tx); err != nil {
					return err
				}
				if err = benchstate.Validate(rows); err != nil {
					return err
				}
				if err = benchstate.ValidateTransition(before, rows); err != nil {
					return err
				}
				for k, b := range rows {
					raw, err := json.Marshal(b)
					if err != nil {
						return err
					}
					if _, err = tx.Exec("INSERT INTO benches VALUES (?,?) ON CONFLICT(key) DO UPDATE SET record=excluded.record", k, string(raw)); err != nil {
						return err
					}
				}
				return nil
			})
		})
	})
}

func daemonGit(ctx context.Context, dir string, args ...string) (string, error) {
	c := gitcontrol.Command(ctx, dir, args...)
	c.Env = gitcontrol.Environment(os.Environ())
	raw, err := c.Output()
	return strings.TrimSpace(string(raw)), err
}

// noBackend reserves scopes but never starts a daemon-side process.
type noBackend struct{}

func (noBackend) Create(string, string) (execution.Identity, error) {
	return execution.Identity{HostID: "fixture-host", BootID: "fixture-boot"}, nil
}
func (noBackend) Start(*exec.Cmd, *execution.Execution) error {
	return errors.New("no execution expected")
}
func (noBackend) Fence(*execution.Execution) error { return nil }
func (noBackend) Empty(*execution.Execution) error { return nil }

func (f *fixture) selector(b *model.Bench) (freshtree.Tree, error) {
	if b.Path == f.payload.root {
		return f.payload, nil
	}
	return nil, nil
}

func (f *fixture) manager(fresh freshtree.Selector) workspaceprep.Manager {
	update := func(change func(benchstate.Database, *sql.Tx) error) error {
		return f.within(func(u workspace.Update) error { return u(change) })
	}
	return workspaceprep.Manager{State: f.state, Data: f.data, CgroupRoot: filepath.Join(filepath.Dir(f.state), "no-cgroups"), Backend: noBackend{}, Update: update, Read: func(fn func(benchstate.Database) error) error {
		return statestore.Read(f.state, func(db *sql.DB) error {
			rows, err := benchstate.Load(db)
			if err != nil {
				return err
			}
			return fn(rows)
		})
	}, Gate: func(_ string, fn func() error) error { f.gate.Lock(); defer f.gate.Unlock(); return fn() }, Git: daemonGit, Admit: func() error { return nil }, Quota: func(*model.Bench) error { return errLegacyQuota }, FreshQuota: f.freshQuota, ReserveLog: func(benchstate.Database) (uint64, error) { return 4096, nil }, Seal: func(string, *output.Capture, *int, bool) error { return errors.New("no execution expected") }, Fresh: fresh}
}

func (f *fixture) lifecycle(fresh freshtree.Selector) workspace.Lifecycle {
	m := f.manager(fresh)
	return workspace.Lifecycle{State: f.state, Data: f.data, Git: daemonGit, Within: f.within, Prepared: m.Prepared, Released: workspaceprep.ReleaseHistory, Admit: func() error { return nil }, Allocate: func(*model.Bench) error { return errLegacyQuota }, FreshQuota: f.freshQuota, Stopped: func(*model.Bench) error { return nil }, Fresh: fresh}
}

// newFixture stages a workspace whose Fresh effect builds the payload layout
// the helper does: independent Git admins, the registered remote URL and the
// task branch, then a root the daemon cannot enter.
func newFixture(t *testing.T) *fixture {
	if os.Geteuid() == 0 {
		t.Skip("mode-0000 privacy needs an unprivileged daemon uid")
	}
	root, err := filepath.EvalSymlinks(t.TempDir()) // registered roots are canonical
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, state: filepath.Join(root, "state"), data: filepath.Join(root, "data")}
	spec := workspace.Spec{Key: "card", CheckpointRepository: "a"}
	for _, name := range []string{"a", "b"} {
		repo := filepath.Join(root, name)
		must(t, root, "init", "-b", "main", repo)
		if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		must(t, repo, "add", ".")
		must(t, repo, "commit", "-m", "fixture")
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: name, Repository: repo})
	}
	f.payload = &payload{t: t, root: filepath.Join(f.data, "benches", "card"), calls: map[string]int{}}
	t.Cleanup(func() { _ = os.Chmod(f.payload.root, 0700) })
	m := workspace.Manager{State: f.state, Data: f.data, Git: daemonGit, Within: f.within, Admit: func() error { return nil }, Allocate: func(*model.Bench) error { return errors.New("fresh staging never allocates through the legacy worker") }}
	m.Fresh = func(_ context.Context, b *model.Bench) error {
		for _, dir := range []string{b.Path, filepath.Join(b.Path, ".superbench-git"), filepath.Join(b.Path, ".superbench-runtime")} {
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}
		}
		for _, member := range b.Workspace.Members {
			mirror := repository.Path(f.data, member.Repository)
			if _, err := git(b.Path, "clone", "--template=", "--no-local", "--no-hardlinks", "--no-checkout", "--separate-git-dir", freshtree.Admin(b, member.Name), "--", mirror, member.Path); err != nil {
				return err
			}
			if _, err := git(member.Path, "config", "--local", "remote.origin.url", member.Repository); err != nil {
				return err
			}
			if _, err := git(member.Path, "checkout", "--no-guess", "-b", member.Branch, member.Source, "--"); err != nil {
				return err
			}
		}
		b.ProjectID, b.QuotaBytes, b.QuotaInodes = 10000, 1<<30, 1000
		return os.Chmod(b.Path, 0)
	}
	staged, err := m.Stage(context.Background(), "create-1", spec)
	if err != nil {
		t.Fatal(err)
	}
	f.staged = staged
	return f
}

func (f *fixture) initialize(fresh freshtree.Selector) (*model.Bench, error) {
	prepared, err := f.manager(fresh).PrepareAcquired(context.Background(), f.staged, "", false)
	if err != nil {
		return prepared, err
	}
	return f.lifecycle(fresh).FinishInitialization(context.Background(), "card", f.staged.Lease.Token)
}

func TestDaemonDirectAccessCannotEnterPrivateFreshTree(t *testing.T) {
	f := newFixture(t)
	_, err := f.initialize(nil)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("legacy access did not meet the payload-private tree: %v", err)
	}
	if len(f.payload.calls) != 0 {
		t.Fatalf("legacy access used the payload route: %v", f.payload.calls)
	}
}

func TestFreshCreateInitializesThroughPayloadRoute(t *testing.T) {
	f := newFixture(t)
	ready, err := f.initialize(f.selector)
	if err != nil {
		t.Fatalf("fresh initialization: %v (payload calls %v)", err, f.payload.calls)
	}
	if ready.Workspace.Phase != "ready" || ready.Lease != nil || ready.Workspace.Checkpoint == nil || len(ready.Workspace.Preparation) != 2 {
		t.Fatalf("fresh workspace not ready: %+v", ready.Workspace)
	}
	for _, kind := range []string{"root", "git", "inputs"} {
		if f.payload.calls[kind] == 0 {
			t.Fatalf("initialization bypassed the payload %s route: %v", kind, f.payload.calls)
		}
	}
	if f.payload.exported != 0 {
		t.Fatal("genesis checkpoint exported commits its mirror already holds")
	}
	for i, member := range ready.Workspace.Members {
		remote := must(t, member.Repository, "rev-parse", "refs/heads/"+member.Branch)
		if remote != ready.Workspace.Checkpoint.Members[i].Commit || remote != member.Source {
			t.Fatalf("member %s genesis checkpoint not on its remote", member.Name)
		}
	}
	info, err := os.Lstat(f.payload.root)
	if err != nil || info.Mode().Perm() != 0 {
		t.Fatalf("payload root privacy changed: %v %v", info, err)
	}

	if f.verified != 1 {
		t.Fatalf("genesis checkpoint verified its allocation %d times, want 1 before export", f.verified)
	}
	f.verified = 0
	// A later checkpoint of payload-made commits imports only what the mirror lacks.
	l := f.lifecycle(f.selector)
	held, err := l.Acquire(context.Background(), "card", "writer")
	if err != nil {
		t.Fatal(err)
	}
	if f.verified != 1 {
		t.Fatalf("fresh acquisition verified its allocation %d times, want 1 read-only check", f.verified)
	}
	if _, err = f.manager(f.selector).PrepareAcquired(context.Background(), held, "", false); err != nil {
		t.Fatal(err)
	}
	member := held.Workspace.Members[1]
	if err = f.payload.as("work", func() error {
		if err := os.WriteFile(filepath.Join(member.Path, "work.txt"), []byte("payload work\n"), 0600); err != nil {
			return err
		}
		if _, err := git(member.Path, "add", "work.txt"); err != nil {
			return err
		}
		_, err := git(member.Path, "commit", "-m", "payload work")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Root's verify refuses a leaf outside its project: nothing is exported.
	f.quotaErr = errors.New("bench leaf is outside its quota project")
	verified := f.verified
	if _, err = l.Checkpoint(context.Background(), "card", held.Lease.Token); !errors.Is(err, f.quotaErr) || f.verified != verified+1 || f.payload.exported != 0 {
		t.Fatalf("checkpoint exported without root's storage verify: %v (verified %d, exported %d)", err, f.verified-verified, f.payload.exported)
	}
	f.quotaErr = nil
	checkpointed, err := l.Checkpoint(context.Background(), "card", held.Lease.Token)
	if err != nil {
		t.Fatalf("fresh checkpoint: %v", err)
	}
	if f.payload.exported != 1 {
		t.Fatalf("exported %d bundles, want only the changed member", f.payload.exported)
	}
	head := checkpointed.Workspace.Checkpoint.Members[1].Commit
	if head == member.Source || must(t, member.Repository, "rev-parse", "refs/heads/"+member.Branch) != head {
		t.Fatal("payload commit did not reach its remote task branch")
	}
	if refs := must(t, member.Mirror, "for-each-ref", "--format=%(refname)", "refs/heads/"+member.Branch); refs != "" {
		t.Fatalf("import created a mirror ref: %q", refs)
	}
	if entries, err := os.ReadDir(filepath.Join(f.state, "fresh-imports")); err != nil || len(entries) != 0 {
		t.Fatalf("import staging retained bundles: %v %v", entries, err)
	}
	if _, err = l.Release(context.Background(), "card", held.Lease.Token); err != nil {
		t.Fatalf("fresh release: %v", err)
	}
}

func TestFreshCleanCheckpointRefusesPayloadDirtyWork(t *testing.T) {
	f := newFixture(t)
	if _, err := f.initialize(f.selector); err != nil {
		t.Fatal(err)
	}
	l := f.lifecycle(f.selector)
	held, err := l.Acquire(context.Background(), "card", "writer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager(f.selector).PrepareAcquired(context.Background(), held, "", false); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"member": filepath.Join(held.Workspace.Members[0].Path, "untracked"), "root": filepath.Join(held.Path, "stray")} {
		if err = f.payload.as("work", func() error { return os.WriteFile(path, nil, 0600) }); err != nil {
			t.Fatal(err)
		}
		if _, err = l.Checkpoint(context.Background(), "card", held.Lease.Token); !errors.Is(err, model.ErrDirty) {
			t.Fatalf("%s: dirty payload work was checkpointed: %v", name, err)
		}
		if err = f.payload.as("work", func() error { return os.Remove(path) }); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fixture) configure(member int, config string) {
	f.t.Helper()
	if err := f.payload.as("work", func() error {
		dir := filepath.Join(f.staged.Workspace.Members[member].Path, ".bench")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0600)
	}); err != nil {
		f.t.Fatal(err)
	}
}

func TestFreshSetupRoutesThroughPayloadTransportOnly(t *testing.T) {
	f := newFixture(t)
	transport := errors.New("payload transport selected")
	tree := &routed{payload: f.payload, transport: transport}
	sel := func(*model.Bench) (freshtree.Tree, error) { return tree, nil }
	f.configure(0, "[setup]\nonce = 'echo once > out'\n")
	_, err := f.manager(sel).PrepareAcquired(context.Background(), f.staged, "", false)
	if !errors.Is(err, transport) || tree.environment != nil {
		t.Fatalf("setup did not use only the payload transport: %v (daemon environment entries: %d)", err, len(tree.environment))
	}
	if f.verified != 1 {
		t.Fatalf("fresh setup verified its allocation %d times, want 1 read-only check", f.verified)
	}

	f = newFixture(t)
	f.configure(0, "[caches]\nnpm = false\n[setup]\nonce = 'true'\n")
	if _, err = f.manager(f.selector).PrepareAcquired(context.Background(), f.staged, "", false); !errors.Is(err, freshtree.ErrCache) {
		t.Fatalf("explicit cache settings were not refused: %v", err)
	}
}

// A Fresh run checks its pinned allocation read-only before the payload
// launch; the legacy apply the helper refuses is never reached.
func TestFreshRunVerifiesQuotaReadOnlyBeforeLaunch(t *testing.T) {
	f := newFixture(t)
	if _, err := f.initialize(f.selector); err != nil {
		t.Fatal(err)
	}
	held, err := f.lifecycle(f.selector).Acquire(context.Background(), "card", "writer")
	if err != nil {
		t.Fatal(err)
	}
	transport := errors.New("payload transport selected")
	sel := func(*model.Bench) (freshtree.Tree, error) {
		return &routed{payload: f.payload, transport: transport}, nil
	}
	f.verified = 0
	_, err = f.manager(sel).Run(context.Background(), "card", held.Lease.Token, "", []string{"true"}, false, nil)
	if !errors.Is(err, transport) {
		t.Fatalf("fresh run did not reach its payload launch: %v", err)
	}
	if f.verified != 1 {
		t.Fatalf("fresh run verified its allocation %d times, want 1 read-only check", f.verified)
	}
}

// routed records the stage environment the daemon would have supplied.
type routed struct {
	*payload
	transport   error
	environment []string
}

func (r *routed) Transport(*model.Bench, string) (stageexec.PrepareTransport, error) {
	return func(cmd *exec.Cmd, _ *execution.Execution) (stageexec.Transport, error) {
		r.environment = cmd.Env
		return nil, r.transport
	}, nil
}

func register(t *testing.T, state, key, id string) {
	t.Helper()
	dir := filepath.Join(state, "payload-layouts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"Version": "superbench.payload-layout.v1", "Key": key, "ID": id})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, key+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSelectionComesFromTheRegistrationAndFailsClosed(t *testing.T) {
	state := t.TempDir()
	b := &model.Bench{Key: "card", Path: t.TempDir(), Workspace: &model.Workspace{ID: strings.Repeat("a", 32)}}
	for _, legacy := range []*model.Bench{nil, {}, b} {
		if tree, err := freshtree.Select(state, legacy); tree != nil || err != nil {
			t.Fatalf("unregistered workspace left the legacy path: %v %v", tree, err)
		}
	}
	if tree, err := freshtree.Select("", b); tree != nil || err != nil {
		t.Fatalf("a caller without state left the legacy path: %v %v", tree, err)
	}
	// Ownership no longer decides: a daemon-owned root with a registration is
	// Fresh, and without an installed helper route it refuses, never legacy.
	register(t, state, "card", b.Workspace.ID)
	if tree, err := freshtree.Select(state, b); tree != nil || !errors.Is(err, freshtree.ErrUnavailable) {
		t.Fatalf("registered workspace without its helper route did not refuse: %v %v", tree, err)
	}
	if fresh, err := freshtree.Selected(nil, state, b); fresh || !errors.Is(err, freshtree.ErrUnavailable) {
		t.Fatalf("Selected hid the refusal: %v %v", fresh, err)
	}
	register(t, state, "card", strings.Repeat("b", 32))
	if registered, err := freshtree.Registered(state, b); !registered || !errors.Is(err, freshtree.ErrUnavailable) {
		t.Fatalf("a registration for another workspace under this key was accepted: %v %v", registered, err)
	}
	if err := os.WriteFile(filepath.Join(state, "payload-layouts", "card.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if registered, err := freshtree.Registered(state, b); !registered || err == nil {
		t.Fatalf("a malformed registration was not refused: %v %v", registered, err)
	}
}

func TestConfigAndFrozenMatchLegacyInputs(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("[setup]\nonce = 'true'\n")
	if err := os.MkdirAll(filepath.Join(dir, ".bench"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".bench", "config.toml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	p := &payload{t: t, root: t.TempDir(), calls: map[string]int{}}
	in, err := p.Inputs(context.Background(), &model.Bench{Path: filepath.Dir(dir)}, filepath.Base(dir))
	if err != nil {
		t.Fatal(err)
	}
	c, digest, err := freshtree.Config(in)
	if err != nil || c == nil || !c.Hooks() {
		t.Fatalf("observed config: %v %v", c, err)
	}
	legacy, legacyDigest, err := freshtree.Inputs(context.Background(), nil, "", &model.Bench{}, model.WorkspaceMember{Path: dir})
	if err != nil || legacyDigest != digest || legacy.Digest != c.Digest {
		t.Fatalf("fresh digest %s differs from legacy %s: %v", digest, legacyDigest, err)
	}
	if err = freshtree.Frozen(in); !errors.Is(err, setup.ErrNoLockfile) {
		t.Fatalf("a member without a lockfile needs no frozen setup: %v", err)
	}
	in.Manifests[2] = registeredinspection.InputFile{Name: "package-lock.json", Present: true, Bytes: 2, SHA256: strings.Repeat("a", 64)}
	if err = freshtree.Frozen(in); !errors.Is(err, freshtree.ErrCache) {
		t.Fatalf("lockfile setup was not refused without a cache transport: %v", err)
	}
	if _, err = freshtree.Environment(nil, nil); err != nil {
		t.Fatal(err)
	}
}

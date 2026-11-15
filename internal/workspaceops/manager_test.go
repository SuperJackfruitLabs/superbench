package workspaceops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitcontrol.Command(context.Background(), dir, args...)
	cmd.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func fixture(t *testing.T) (Manager, protocolv2.Request) {
	t.Helper()
	root := t.TempDir()
	m := Manager{Store: bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}}
	yes := true
	r := protocolv2.Request{Version: protocolv2.Version, Operation: "create", Key: "card", RequestID: "creation", Initialize: &yes, CheckpointRepository: "api"}
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
		r.Repositories = append(r.Repositories, protocolv2.Repository{Name: name, Repository: repo})
	}
	return m, r
}

func create(t *testing.T, m Manager, r protocolv2.Request) *Result {
	t.Helper()
	out, err := m.Execute(context.Background(), r, 42)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || out.Workspace.Workspace.Phase != "ready" || out.Workspace.Lease != nil || out.GrantToken != "" || !out.RootPath || len(out.Preparation) != 2 {
		t.Fatal("creation omitted complete readiness or exposed ownership", out)
	}
	return out
}

func request(op string) protocolv2.Request {
	return protocolv2.Request{Version: protocolv2.Version, Operation: op, Key: "card", Holder: "worker", RequestID: "request-" + op}
}

func snapshot(t *testing.T, m Manager) []bench.Bench {
	t.Helper()
	rows, err := m.Store.Snapshot("card")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestCreateIdentityReplayCannotRecoverLaterHolderCapability(t *testing.T) {
	m, r := fixture(t)
	first := create(t, m, r)
	id, err := CreationID(r, 42)
	if err != nil || id != first.Workspace.Workspace.CreationID {
		t.Fatal("creation identity not pinned to request", err)
	}
	otherUID, _ := CreationID(r, 43)
	changed := r
	changed.RequestID = "different"
	otherRequest, _ := CreationID(changed, 42)
	if id == otherUID || id == otherRequest {
		t.Fatal("creation ID ignored peer or request")
	}
	held, err := m.Execute(context.Background(), request("lease"), 42)
	if err != nil || held == nil || held.GrantToken == "" || !held.RootPath {
		t.Fatal("lease did not grant owner", err)
	}
	if held.Workspace.Lease.Token != "" || held.Workspace.Lease.Continuation != "" {
		t.Fatal("private fields escaped inventory")
	}
	replay, err := m.Execute(context.Background(), r, 42)
	if err != nil || replay == nil || replay.GrantToken != "" || replay.RootPath || replay.Workspace.Lease.Token != "" {
		t.Fatal("creation replay granted later owner", err)
	}
	if err := m.Store.CheckWorkspaceOwner("card", held.GrantToken, "worker"); err != nil {
		t.Fatal("replay changed current owner", err)
	}
	if out, err := m.Execute(context.Background(), r, 43); err == nil || out != nil {
		t.Fatal("other peer resumed creation")
	}
}

func TestSetupAcquiresOnceCompletesAndDoesNotReexportSuppliedCapability(t *testing.T) {
	m, r := fixture(t)
	create(t, m, r)
	setup := request("setup")
	out, err := m.Execute(context.Background(), setup, 42)
	if err != nil || out == nil || out.Workspace.Lease != nil || out.GrantToken != "" || len(out.Preparation) != 2 {
		t.Fatal("setup did not checkpoint and release", err)
	}
	for _, p := range out.Preparation {
		if p.Status != "prepared" {
			t.Fatal("setup proof missing", p)
		}
	}
	held, err := m.Execute(context.Background(), request("lease"), 42)
	if err != nil {
		t.Fatal(err)
	}
	setup.LeaseToken, setup.RepositoryName = held.GrantToken, "api"
	out, err = m.Execute(context.Background(), setup, 42)
	if err != nil || out == nil || out.GrantToken != "" || out.Workspace.Lease != nil || len(out.Preparation) != 1 || out.Preparation[0].Name != "api" {
		t.Fatal("selected setup leaked capability or wrong outcome selection", err)
	}
}

func TestPartialInitializationSetupRequiresWholeProofVectorBeforeReadiness(t *testing.T) {
	m, r := fixture(t)
	staged, err := m.Store.StageWorkspaceContext(context.Background(), "staged-initialization", creationSpec(r))
	if err != nil {
		t.Fatal(err)
	}
	setup := request("setup")
	setup.Holder, setup.LeaseToken, setup.RepositoryName = "initialization", staged.Lease.Token, "api"
	partial, err := m.Execute(context.Background(), setup, 42)
	if !errors.Is(err, ErrConflict) || partial == nil || partial.Workspace.Workspace.Phase != "initializing" || partial.Workspace.Workspace.Checkpoint != nil || partial.GrantToken != "" || len(partial.Preparation) != 1 || partial.Preparation[0].Status != "prepared" {
		t.Fatal("partial init fabricated readiness or reexported token", err)
	}
	setup.RepositoryName = ""
	ready, err := m.Execute(context.Background(), setup, 42)
	if err != nil || ready == nil || ready.Workspace.Workspace.Phase != "ready" || ready.Workspace.Lease != nil || ready.Workspace.Workspace.Checkpoint == nil || ready.GrantToken != "" {
		t.Fatal("complete init setup did not publish genesis and release", err)
	}
	if err := m.Store.CheckWorkspaceOwner("card", staged.Lease.Token, "initialization"); !errors.Is(err, bench.ErrLease) {
		t.Fatal("old initialization capability survived completion", err)
	}
}

func TestSelectedSetupFailureRetainsOnlyItsNewGrantAndStopRevokesContinuation(t *testing.T) {
	m, r := fixture(t)
	ready := create(t, m, r)
	web := ready.Workspace.Workspace.Members[1]
	if err := os.WriteFile(filepath.Join(web.Path, "source.txt"), []byte("unknown later work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	setup := request("setup")
	setup.RepositoryName = "api"
	out, err := m.Execute(context.Background(), setup, 42)
	if err == nil || out == nil || out.GrantToken == "" || !out.RootPath || out.Workspace.Lease == nil || len(out.Preparation) != 1 || out.Preparation[0].Name != "api" {
		t.Fatal("dirty later member failed to retain authorized owner", err)
	}
	token := out.GrantToken
	if out.Workspace.Lease.Token != "" || out.Workspace.Lease.Continuation != "" {
		t.Fatal("private inventory fields exposed")
	}
	before := snapshot(t, m)
	wrong := setup
	wrong.LeaseToken, wrong.Holder = token, "another-worker"
	if out, err := m.Execute(context.Background(), wrong, 42); !errors.Is(err, bench.ErrLease) || out != nil {
		t.Fatal("same capability accepted wrong holder", err)
	}
	wrong.Holder, wrong.LeaseToken = "worker", strings.Repeat("f", 32)
	if out, err := m.Execute(context.Background(), wrong, 42); !errors.Is(err, bench.ErrLease) || out != nil {
		t.Fatal("wrong token returned private result", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, m)) {
		t.Fatal("wrong owner changed state")
	}
	admin := protocolv2.Request{Version: protocolv2.Version, Operation: "stop_admin", Key: "card"}
	stopped, err := m.Execute(context.Background(), admin, 42)
	if err != nil || stopped == nil || stopped.GrantToken != token || !stopped.Workspace.Lease.StopRequested || stopped.Workspace.Lease.Continuation != "" {
		t.Fatal("admin stop did not grant only stopped owner", err)
	}
	setup.LeaseToken = token
	failed, err := m.Execute(context.Background(), setup, 42)
	if err == nil || failed == nil || failed.GrantToken != "" || !failed.Workspace.Lease.StopRequested {
		t.Fatal("stopped reuse revived automatic setup", err)
	}
	if raw, err := os.ReadFile(filepath.Join(web.Path, "source.txt")); err != nil || string(raw) != "unknown later work\n" {
		t.Fatal("setup/stop destroyed unknown work", err)
	}
}

func TestPreflightRejectsWrongKindSelectorAndUnrepresentableGrowthBeforeEffects(t *testing.T) {
	m, r := fixture(t)
	create(t, m, r)
	before := snapshot(t, m)
	bad := request("setup")
	bad.RepositoryName = "absent"
	if err := m.Preflight(bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown member accepted", err)
	}
	bad = request("run")
	bad.Arguments = []string{"/bin/sh", "nul\x00argument"}
	if err := m.Preflight(bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid argument passed acquisition preflight", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, m)) {
		t.Fatal("preflight allocated owner")
	}
	missing := request("lease")
	missing.Key = "missing"
	if err := m.Preflight(missing); !errors.Is(err, bench.ErrNotFound) {
		t.Fatal("missing workspace not typed", err)
	}
	if _, err := m.Store.Create("scalar", bench.Options{Repo: r.Repositories[0].Repository}, false); err != nil {
		t.Fatal(err)
	}
	bad = request("lease")
	bad.Key = "scalar"
	called := false
	if err := m.Store.WithWorkspace("scalar", true, func() error { called = true; return nil }); !errors.Is(err, bench.ErrScalar) || called {
		t.Fatal("scalar reached aggregate side effect", err)
	}
	if err := m.Preflight(bad); !errors.Is(err, bench.ErrScalar) {
		t.Fatal("scalar aggregate preflight not typed", err)
	}
	long := m
	long.Store.Data = "/" + strings.Repeat("x", 4096)
	if err := long.Preflight(r); !errors.Is(err, ErrInvalid) {
		t.Fatal("long root reached filesystem preflight", err)
	}
}

func TestRecoveryUsesExactSourceAndFreshRequestIdentity(t *testing.T) {
	m, r := fixture(t)
	source := create(t, m, r).Workspace
	yes := true
	recover := protocolv2.Request{Version: protocolv2.Version, Operation: "recover", Key: "recovered", RequestID: "recover-source", Initialize: &yes, Repositories: r.Repositories,
		Source: &protocolv2.Source{Repository: r.Repositories[0].Repository, BenchID: source.Workspace.ID, Commit: source.Workspace.Checkpoint.Seal, ManifestSHA256: source.Workspace.Checkpoint.Digest}}
	out, err := m.Execute(context.Background(), recover, 42)
	if err != nil || out == nil || out.Workspace.Lease != nil || out.Workspace.Workspace.ID == source.Workspace.ID || out.Workspace.Workspace.RecoveredFrom == nil || !out.RootPath || out.GrantToken != "" {
		t.Fatal("recovery inherited source identity/owner", err)
	}
	for i, member := range out.Workspace.Workspace.Members {
		if member.Source != source.Workspace.Checkpoint.Members[i].Commit || member.Branch != "bench/recovered" {
			t.Fatal("recovery selected independent task tips")
		}
	}
}

func TestWarmPartialFailureGrantsOnlyMatchingInitializationRequest(t *testing.T) {
	if execution.Supported {
		t.Skip("native unsupported-command path; no managed runtime in ordinary integration tests")
	}
	m, r := fixture(t)
	repo := r.Repositories[1].Repository
	if err := os.Mkdir(filepath.Join(repo, ".bench"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".bench", "config.toml"), []byte("[setup]\neach = 'exit 1'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "requires supervised setup")
	out, err := m.Execute(context.Background(), r, 42)
	if err == nil || out == nil || out.GrantToken == "" || !out.RootPath || out.Workspace.Lease.Holder != "initialization" || out.Workspace.Lease.Token != "" || out.Workspace.Lease.Continuation != "" {
		t.Fatal("warm failure omitted authorized init grant", err)
	}
	token := out.GrantToken
	replay, err := m.Execute(context.Background(), r, 42)
	if err == nil || replay == nil || replay.GrantToken != token {
		t.Fatal("same init request changed its grant", err)
	}
	changed := r
	changed.RequestID = "other-init"
	if out, err := m.Execute(context.Background(), changed, 42); err == nil || out != nil {
		t.Fatal("different creation request captured init grant")
	}
	if err := m.Store.CheckWorkspaceOwner(r.Key, token, "initialization"); err != nil {
		t.Fatal("foreign request changed init owner", err)
	}
	admin := protocolv2.Request{Version: protocolv2.Version, Operation: "stop_admin", Key: r.Key}
	stopped, err := m.Execute(context.Background(), admin, 42)
	if err != nil || stopped == nil || stopped.GrantToken != token || !stopped.Workspace.Lease.StopRequested || stopped.Workspace.Lease.Continuation != "" {
		t.Fatal("admin stop did not preserve revoked initialization owner", err)
	}
	setup := request("setup")
	setup.LeaseToken, setup.Holder = token, "initialization"
	failed, err := m.Execute(context.Background(), setup, 42)
	if err == nil || failed == nil || failed.GrantToken != "" || !failed.Workspace.Lease.StopRequested || failed.Workspace.Lease.Continuation != "" {
		t.Fatal("stopped initialization owner resumed setup", err)
	}
	if _, err := os.Stat(filepath.Join(stopped.Workspace.Path, "web", ".bench", "config.toml")); err != nil {
		t.Fatal("stopped initialization lost source files", err)
	}
}

func TestResultNeverEmbedsPrivateCapabilities(t *testing.T) {
	b := &model.Bench{Workspace: &model.Workspace{}, Lease: &model.Lease{Token: strings.Repeat("1", 32), Continuation: strings.Repeat("2", 32)}}
	out := result(b, strings.Repeat("3", 32), false)
	if out.GrantToken != "" || out.Workspace.Lease.Token != "" || out.Workspace.Lease.Continuation != "" || b.Lease.Token == "" {
		t.Fatal("result leaked or mutated a private capability")
	}
}

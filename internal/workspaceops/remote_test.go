package workspaceops

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerenew"
)

// remoteScopes records fences and lets a test look at the state each one saw.
type remoteScopes struct {
	mu      sync.Mutex
	fenced  []string
	onFence func()
}

func (*remoteScopes) Create(string, string) (execution.Identity, error) {
	return execution.Identity{BootID: "test-boot", HostID: "test-host"}, nil
}
func (s *remoteScopes) Fence(e *execution.Execution) error {
	s.mu.Lock()
	s.fenced = append(s.fenced, e.ID)
	hook := s.onFence
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}
func (*remoteScopes) Empty(*execution.Execution) error           { return nil }
func (*remoteScopes) Present(*execution.Execution) (bool, error) { return true, nil }
func (s *remoteScopes) fences() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.fenced...)
}

type remoteFixture struct {
	m      Manager
	scopes *remoteScopes
	token  string
	grant  remotestate.Grant
}

// newRemote is a ready workspace held by "worker" with a remote execution.
func newRemote(t *testing.T) *remoteFixture {
	t.Helper()
	m, r := fixture(t)
	create(t, m, r)
	s := &remoteScopes{}
	m.RemoteScopes = s
	held, err := m.Execute(context.Background(), request("lease"), 42)
	if err != nil {
		t.Fatal(err)
	}
	f := &remoteFixture{m: m, scopes: s, token: held.GrantToken}
	f.grant, err = f.remote(t).Grant(context.Background(), "card", f.token)
	if err != nil {
		t.Fatal(err)
	}
	if e := snapshot(t, m)[0].Lease.Execution; e == nil || e.Kind != execution.KindRemote {
		t.Fatal("remote execution not recorded", e)
	}
	return f
}

func (f *remoteFixture) remote(t *testing.T) remotestate.Manager {
	t.Helper()
	r, err := remotestate.ForStore(f.m.Store, f.scopes)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *remoteFixture) owned(op string) protocolv2.Request {
	if op == "run" {
		r := request(op)
		r.LeaseToken = f.token
		return r
	}
	return protocolv2.Request{Version: protocolv2.Version, Operation: op, Key: "card", LeaseToken: f.token}
}

func (f *remoteFixture) fencedOnce(t *testing.T) {
	t.Helper()
	if got := f.scopes.fences(); len(got) != 1 || got[0] != f.grant.Claims.ExecutionID {
		t.Fatal("remote scope fences", got)
	}
	if _, err := f.remote(t).Admit(f.grant.Claims); !errors.Is(err, remotestate.ErrRevoked) {
		t.Fatal("claims admitted after the lease moved", err)
	}
}

func TestRemoteExecutionFencedBeforeCheckpoint(t *testing.T) {
	f := newRemote(t)
	before := snapshot(t, f.m)[0].Workspace.Checkpoint
	f.scopes.onFence = func() {
		b := snapshot(t, f.m)[0]
		if b.Workspace.Checkpoint != nil && before != nil && b.Workspace.Checkpoint.Digest != before.Digest || b.Workspace.Pending != nil || b.Lease.Execution == nil {
			t.Error("checkpoint began before the remote scope was fenced")
		}
	}
	out, err := f.m.Execute(context.Background(), f.owned("checkpoint"), 42)
	if err != nil || out == nil {
		t.Fatal("checkpoint", err)
	}
	b := snapshot(t, f.m)[0]
	if b.Lease == nil || b.Lease.Execution != nil {
		t.Fatal("remote execution left on the lease", b.Lease)
	}
	f.fencedOnce(t)
}

func TestRemoteExecutionFencedBeforeRelease(t *testing.T) {
	f := newRemote(t)
	if _, err := f.m.Execute(context.Background(), f.owned("release"), 42); err != nil {
		t.Fatal("release", err)
	}
	if b := snapshot(t, f.m)[0]; b.Lease != nil {
		t.Fatal("lease not released", b.Lease)
	}
	f.fencedOnce(t)
}

func TestRemoteExecutionFencedByStopAndStopAdmin(t *testing.T) {
	f := newRemote(t)
	if _, err := f.m.Execute(context.Background(), f.owned("stop"), 42); err != nil {
		t.Fatal("stop", err)
	}
	if b := snapshot(t, f.m)[0]; b.Lease == nil || b.Lease.Execution != nil || !b.Lease.StopRequested {
		t.Fatal("stop left the remote execution", b.Lease)
	}
	f.fencedOnce(t)

	f = newRemote(t)
	admin := protocolv2.Request{Version: protocolv2.Version, Operation: "stop_admin", Key: "card"}
	out, err := f.m.Execute(context.Background(), admin, 42)
	if err != nil || out == nil || out.GrantToken == "" {
		t.Fatal("stop_admin", err)
	}
	if b := snapshot(t, f.m)[0]; b.Lease == nil || b.Lease.Execution != nil || !b.Lease.StopRequested {
		t.Fatal("stop_admin left the remote execution", b.Lease)
	}
	f.fencedOnce(t)
}

func TestRemoteFenceFailureRefusesTheOperation(t *testing.T) {
	f := newRemote(t)
	f.m.RemoteScopes = &failingScopes{}
	before := snapshot(t, f.m)
	for _, r := range []protocolv2.Request{f.owned("checkpoint"), f.owned("release"), f.owned("stop"), {Version: protocolv2.Version, Operation: "stop_admin", Key: "card"}} {
		out, err := f.m.Execute(context.Background(), r, 42)
		if !errors.Is(err, remotestate.ErrUnavailable) || !strings.Contains(err.Error(), "cgroup.kill refused") || out != nil {
			t.Fatal(r.Operation, "did not stop at the failed fence", err)
		}
		if after := snapshot(t, f.m); !reflect.DeepEqual(before, after) {
			t.Fatal(r.Operation, "changed the lease after a failed fence")
		}
	}
}

type failingScopes struct{ remoteScopes }

func (*failingScopes) Fence(*execution.Execution) error { return errors.New("cgroup.kill refused") }

func TestRunRefusedWhileRemoteExecution(t *testing.T) {
	f := newRemote(t)
	run := f.owned("run")
	run.Arguments = []string{"true"}
	if _, err := f.m.Execute(context.Background(), run, 42); err == nil {
		t.Fatal("run started beside a remote execution")
	}
	if e := snapshot(t, f.m)[0].Lease.Execution; e == nil || e.Kind != execution.KindRemote || e.ID != f.grant.Claims.ExecutionID || e.Phase != "running" {
		t.Fatal("run replaced the remote execution", e)
	}
}

func TestRenewAdminInvalidatesRemoteClaims(t *testing.T) {
	f := newRemote(t)
	admin := protocolv2.Request{Version: protocolv2.Version, Operation: "stop_admin", Key: "card"}
	if _, err := f.m.Execute(context.Background(), admin, 42); err != nil {
		t.Fatal(err)
	}
	repair, err := f.m.Store.WorkspaceRepair()
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := workspacerenew.Manager{Prep: repair.Prep}.Renew("card", 0)
	if err != nil {
		t.Fatal(err)
	}
	old := f.grant.Claims
	if renewed.Lease.ID != old.LeaseID || renewed.Lease.Generation != old.Generation+1 {
		t.Fatal("renewal identity", renewed.Lease.ID, renewed.Lease.Generation)
	}
	r := f.remote(t)
	if _, err := r.Admit(old); !errors.Is(err, remotestate.ErrRevoked) {
		t.Fatal("old claims admitted after renew_admin", err)
	}
	g, err := r.Grant(context.Background(), "card", renewed.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if g.Claims.Generation != old.Generation+1 || g.Claims.LeaseID != old.LeaseID || g.Claims == old {
		t.Fatal("new grant claims", g.Claims)
	}
	sameIdentity := remoteca.Claims{BenchID: old.BenchID, LeaseID: old.LeaseID, Generation: old.Generation, ExecutionID: g.Claims.ExecutionID}
	if _, err := r.Admit(sameIdentity); !errors.Is(err, remotestate.ErrRevoked) {
		t.Fatal("old generation admitted with the live execution", err)
	}
}

// countRemoteState counts every opening of the remote state manager, which
// is what takes the extra host-lock sessions, gate and Git-control recovery.
func countRemoteState(t *testing.T) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	real := remoteFor
	remoteFor = func(store bench.Store, scopes remotestate.Scopes) (remotestate.Manager, error) {
		calls.Add(1)
		return real(store, scopes)
	}
	t.Cleanup(func() { remoteFor = real })
	return &calls
}

// Without a remote execution, checkpoint, release, stop and stop_admin never
// open the remote state manager: remote access off costs them nothing.
func TestNoRemoteExecutionOpensNoRemoteState(t *testing.T) {
	m, r := fixture(t)
	create(t, m, r)
	s := &remoteScopes{}
	m.RemoteScopes = s
	calls := countRemoteState(t)
	owned := func(op, token string) protocolv2.Request {
		return protocolv2.Request{Version: protocolv2.Version, Operation: op, Key: "card", LeaseToken: token}
	}
	lease := func() string {
		held, err := m.Execute(context.Background(), request("lease"), 42)
		if err != nil {
			t.Fatal(err)
		}
		return held.GrantToken
	}
	token := lease()
	for _, op := range []string{"checkpoint", "release"} {
		if _, err := m.Execute(context.Background(), owned(op, token), 42); err != nil {
			t.Fatal(op, err)
		}
	}
	if _, err := m.Execute(context.Background(), owned("stop", lease()), 42); err != nil {
		t.Fatal("stop", err)
	}
	if _, err := m.Execute(context.Background(), protocolv2.Request{Version: protocolv2.Version, Operation: "stop_admin", Key: "card"}, 42); err != nil {
		t.Fatal("stop_admin", err)
	}
	if n := calls.Load(); n != 0 || len(s.fences()) != 0 {
		t.Fatalf("remote state opened %d times, fences %v, with no remote execution", n, s.fences())
	}
}

// A remote execution is fenced whatever the activation says: one recorded
// before an operator turned remote access off is still fenced on release.
// workspaceops has no switch to consult, so this is the lease row alone.
func TestLeftoverRemoteExecutionIsStillFenced(t *testing.T) {
	f := newRemote(t)
	calls := countRemoteState(t)
	if _, err := f.m.Execute(context.Background(), f.owned("release"), 42); err != nil {
		t.Fatal("release", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("remote state opened %d times for one remote execution", n)
	}
	f.fencedOnce(t)
}

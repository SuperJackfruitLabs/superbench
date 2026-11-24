package remotestate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteschema"
)

func session(t *testing.T, f *fixture, g Grant) int64 {
	t.Helper()
	a, err := f.m.Admit(g.Claims)
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.m.SessionStarted(a, remoteschema.SessionRecord{Serial: 9, Fingerprint: "SHA256:client", Source: "100.64.0.9:50000", Kind: "shell"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestFenceKillsScopeThenClearsExecution(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t)
	open := session(t, f, g)
	if err := f.m.Fence(context.Background(), "card", f.token, false); err != nil {
		t.Fatal(err)
	}
	if got := f.scopes.log(); !reflect.DeepEqual(got, []string{"create:" + g.Claims.ExecutionID, "fence:" + g.Claims.ExecutionID}) {
		t.Fatal("scope calls", got)
	}
	b := f.record(t)
	if b.Lease == nil || b.Lease.Execution != nil || b.Lease.Token != f.token {
		t.Fatal("remote execution not cleared, or the lease changed", b.Lease)
	}
	s, err := f.m.Status("card", 10)
	if err != nil || len(s.Sessions) != 1 || s.Sessions[0].ID != open || s.Sessions[0].EndReason != "fenced" || len(s.Remote) != 0 {
		t.Fatal("open session not ended as fenced", s, err)
	}
	if _, err := f.m.Admit(g.Claims); !errors.Is(err, ErrRevoked) {
		t.Fatal("claims admitted after fence", err)
	}
	if err := f.m.SessionEnded(open, 10, 20, "closed"); err != nil {
		t.Fatal("ending a fenced session failed", err)
	}
	if s, _ = f.m.Status("card", 10); s.Sessions[0].EndReason != "fenced" {
		t.Fatal("fence reason overwritten", s.Sessions[0])
	}
}

// workspaceprep's stop marks any recorded execution stopped. A stopped remote
// execution is still cleared, or release would wait on history it never has.
func TestFenceClearsStoppedRemoteExecution(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t)
	prep := f.m.Prep
	prep.Backend = f.scopes
	if _, err := prep.Stop("card", f.token, false); err != nil {
		t.Fatal(err)
	}
	if e := f.record(t).Lease.Execution; e == nil || e.Phase != "stopped" {
		t.Fatal("stop did not mark the remote execution stopped", e)
	}
	if err := f.m.Fence(context.Background(), "card", "", true); err != nil {
		t.Fatal(err)
	}
	if e := f.record(t).Lease.Execution; e != nil {
		t.Fatal("stopped remote execution not cleared", e)
	}
	if got := f.scopes.log(); got[len(got)-1] != "fence:"+g.Claims.ExecutionID {
		t.Fatal("scope calls", got)
	}
}

func TestFenceWithoutRemoteIsNoop(t *testing.T) {
	f := newFixture(t)
	if err := f.m.Fence(context.Background(), "card", f.token, false); err != nil {
		t.Fatal(err)
	}
	run := &model.Execution{ID: strings.Repeat("2", 32), Root: "/sys/fs/cgroup/superbench", BootID: "test-boot", HostID: "test-host", Phase: "starting", Log: "/srv/state/run.log", LogLimitBytes: 4096}
	f.change(t, func(b *model.Bench) { b.Lease.Execution = run })
	before := f.record(t)
	for _, admin := range []bool{false, true} {
		if err := f.m.Fence(context.Background(), "card", "", admin); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.m.Fence(context.Background(), "missing", "", true); err != nil {
		t.Fatal(err)
	}
	if len(f.scopes.log()) != 0 || !reflect.DeepEqual(before, f.record(t)) {
		t.Fatal("fence touched a run execution", f.scopes.log())
	}
}

func TestFenceFailureKeepsExecution(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t)
	open := session(t, f, g)
	before := f.record(t)
	f.scopes.fail = errors.New("cgroup.kill refused")
	if err := f.m.Fence(context.Background(), "card", f.token, false); !errors.Is(err, ErrUnavailable) {
		t.Fatal("failed fence reported", err)
	}
	if !reflect.DeepEqual(before, f.record(t)) {
		t.Fatal("failed fence changed state")
	}
	if s, err := f.m.Status("card", 10); err != nil || s.Sessions[0].ID != open || s.Sessions[0].EndReason != "" {
		t.Fatal("failed fence ended sessions", s, err)
	}
	if _, err := f.m.Admit(g.Claims); err != nil {
		t.Fatal("failed fence revoked claims without killing the scope", err)
	}
}

func TestFenceRefusesAnotherOwner(t *testing.T) {
	f := newFixture(t)
	f.grant(t)
	before := f.record(t)
	if err := f.m.Fence(context.Background(), "card", strings.Repeat("0", 48), false); !errors.Is(err, bench.ErrLease) {
		t.Fatal("fence with a wrong token", err)
	}
	if len(f.scopes.log()) != 1 || !reflect.DeepEqual(before, f.record(t)) {
		t.Fatal("refused fence had effects", f.scopes.log())
	}
}

// A new grant's scope replaced the fenced one while the kill ran (a writer
// that does not take the gate): the fence must not clear the new one.
func TestFenceKeepsAnExecutionReplacedDuringTheKill(t *testing.T) {
	f := newFixture(t)
	f.grant(t)
	next := &model.Execution{ID: strings.Repeat("9", 32), Root: "/sys/fs/cgroup/superbench", BootID: "test-boot", HostID: "test-host", Phase: "running", Kind: "remote"}
	f.scopes.onFence = func() { f.change(t, func(b *model.Bench) { b.Lease.Execution = next }) }
	if err := f.m.Fence(context.Background(), "card", f.token, false); !errors.Is(err, ErrConflict) {
		t.Fatal("fence over a replaced execution", err)
	}
	if e := f.record(t).Lease.Execution; e == nil || e.ID != next.ID {
		t.Fatal("fence cleared the replacement", e)
	}
}

package remotestate

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
)

func TestGrantRecordsRemoteExecution(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t)
	b := f.record(t)
	e := b.Lease.Execution
	if e == nil || e.Kind != execution.KindRemote || e.Phase != "running" || e.Root != f.m.root() || e.BootID != "test-boot" || e.HostID != "test-host" || e.PID != 0 || e.Log != "" {
		t.Fatal("remote execution not recorded", e)
	}
	if got := f.scopes.log(); !reflect.DeepEqual(got, []string{"create:" + e.ID}) {
		t.Fatal("scope calls", got)
	}
	want := remoteca.Claims{BenchID: b.Workspace.ID, LeaseID: b.Lease.ID, Generation: b.Lease.Generation, ExecutionID: e.ID}
	if g.Claims != want || g.Key != "card" || g.Holder != "worker" || want.LeaseID == "" || want.Generation == 0 {
		t.Fatal("grant claims", g, want)
	}
}

func TestGrantReusesLiveRemoteExecution(t *testing.T) {
	f := newFixture(t)
	first := f.grant(t)
	second := f.grant(t)
	if second.Claims != first.Claims || f.scopes.creates() != 1 {
		t.Fatal("live remote execution not reused", first, second, f.scopes.log())
	}
}

func TestGrantRefusalsHaveNoEffects(t *testing.T) {
	run := &model.Execution{ID: strings.Repeat("2", 32), Root: "/sys/fs/cgroup/superbench", BootID: "test-boot", HostID: "test-host", Phase: "starting", Log: "/srv/state/run.log", LogLimitBytes: 4096}
	for name, tc := range map[string]struct {
		token  func(*fixture) string
		change func(*model.Bench)
		want   error
	}{
		"wrong-token":   {token: func(*fixture) string { return strings.Repeat("0", 48) }, want: bench.ErrLease},
		"empty-token":   {token: func(*fixture) string { return "" }, want: bench.ErrLease},
		"stopped":       {change: func(b *model.Bench) { b.Lease.StopRequested, b.Lease.Continuation = true, "" }, want: ErrConflict},
		"run-execution": {change: func(b *model.Bench) { b.Lease.Execution = run }, want: ErrConflict},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if tc.change != nil {
				f.change(t, tc.change)
			}
			token := f.token
			if tc.token != nil {
				token = tc.token(f)
			}
			before := f.record(t)
			g, err := f.m.Grant(context.Background(), "card", token)
			if !errors.Is(err, tc.want) || g != (Grant{}) {
				t.Fatal("grant not refused", err, g)
			}
			if len(f.scopes.log()) != 0 || !reflect.DeepEqual(before, f.record(t)) {
				t.Fatal("refused grant had effects", f.scopes.log())
			}
		})
	}
}

// States the store cannot reach through an update are checked directly: the
// grant's admission is the same function on both sides of the scope call.
func TestGrantableRefusesEveryOtherState(t *testing.T) {
	f := newFixture(t)
	ready := f.record(t)
	for name, change := range map[string]func(*model.Bench){
		"initialization-holder": func(b *model.Bench) { b.Lease.Holder = "initialization" },
		"not-ready":             func(b *model.Bench) { b.Workspace.Phase = "prepared" },
		"pending":               func(b *model.Bench) { c := *b.Workspace.Checkpoint; b.Workspace.Pending = &c },
		"archive":               func(b *model.Bench) { b.Workspace.Archive = &model.WorkspaceArchive{} },
		"transfer":              func(b *model.Bench) { b.Workspace.Transfer = &model.WorkspaceTransfer{} },
		"repair":                func(b *model.Bench) { b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{} },
		"stage":                 func(b *model.Bench) { b.Workspace.Stage = &model.WorkspaceStage{Name: "api"} },
		"no-continuation":       func(b *model.Bench) { b.Lease.Continuation = "" },
		"stop-requested":        func(b *model.Bench) { b.Lease.StopRequested = true },
		"unknown-kind": func(b *model.Bench) {
			b.Lease.Execution = &model.Execution{ID: strings.Repeat("3", 32), Phase: "running", Kind: "shell"}
		},
	} {
		b := *ready
		w, l := *ready.Workspace, *ready.Lease
		b.Workspace, b.Lease = &w, &l
		change(&b)
		if _, err := grantable(map[string]*model.Bench{"card": &b}, nil, "card", f.token); !errors.Is(err, ErrConflict) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Review Focus 3: benchd restarted, so the recorded scope is gone. The next
// grant fences the dead record (a no-op) and records a new scope with a new
// ID, which the old certificate's claims no longer match.
func TestGrantReplacesRemoteExecutionWhoseScopeIsGone(t *testing.T) {
	f := newFixture(t)
	old := f.grant(t)
	if _, err := f.m.Admit(old.Claims); err != nil {
		t.Fatal(err)
	}
	f.scopes.gone(old.Claims.ExecutionID)
	renewed := f.grant(t)
	if renewed.Claims.ExecutionID == old.Claims.ExecutionID || f.scopes.creates() != 2 {
		t.Fatal("dead remote execution reused", renewed, f.scopes.log())
	}
	calls := f.scopes.log()
	want := []string{"create:" + old.Claims.ExecutionID, "present:" + old.Claims.ExecutionID, "fence:" + old.Claims.ExecutionID, "create:" + renewed.Claims.ExecutionID}
	if !reflect.DeepEqual(calls, want) {
		t.Fatal("scope calls", calls)
	}
	if _, err := f.m.Admit(old.Claims); !errors.Is(err, ErrRevoked) {
		t.Fatal("old claims admitted after replacement", err)
	}
	if _, err := f.m.Admit(renewed.Claims); err != nil {
		t.Fatal("new claims refused", err)
	}
	if e := f.record(t).Lease.Execution; e == nil || e.ID != renewed.Claims.ExecutionID || e.Phase != "running" {
		t.Fatal("replacement not recorded", e)
	}
}

// The record changes between the grant's check and its write (a writer that
// does not take the gate): the grant refuses and fences the scope it made.
func TestGrantRefusesARecordChangedDuringCreate(t *testing.T) {
	f := newFixture(t)
	other := &model.Execution{ID: strings.Repeat("9", 32), Root: "/sys/fs/cgroup/superbench", BootID: "test-boot", HostID: "test-host", Phase: "running", Kind: execution.KindRemote}
	f.scopes.onCreate = func() { f.change(t, func(b *model.Bench) { b.Lease.Execution = other }) }
	if g, err := f.m.Grant(context.Background(), "card", f.token); !errors.Is(err, ErrConflict) || g != (Grant{}) {
		t.Fatal("grant over a changed record", g, err)
	}
	calls := f.scopes.log()
	if len(calls) != 2 || calls[1] != "fence:"+strings.TrimPrefix(calls[0], "create:") {
		t.Fatal("created scope not fenced", calls)
	}
	if e := f.record(t).Lease.Execution; e == nil || e.ID != other.ID {
		t.Fatal("grant overwrote the changed record", e)
	}
}

func TestGrantRefusesABoundOperation(t *testing.T) {
	f := newFixture(t)
	b := f.record(t)
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 0, RequestID: "pending-run", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "run", Epoch: strings.Repeat("b", 32)}
	if err := f.m.Prep.Update(func(_ benchstate.Database, tx *sql.Tx) error { return operationcontrol.Begin(tx, i, b.Workspace.ID) }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Grant(context.Background(), "card", f.token); !errors.Is(err, ErrConflict) {
		t.Fatal("grant beside a bound operation", err)
	}
	if len(f.scopes.log()) != 0 || f.record(t).Lease.Execution != nil {
		t.Fatal("refused grant had effects", f.scopes.log())
	}
}

package workspacerenew_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerenew"
)

// The operator path for a dirty stopped lease: stop_admin, renew_admin, a root
// run under the new token that removes the stray file and checkpoints, release.
func TestRenewedOwnerRunsTheFixThenCheckpointsAndReleases(t *testing.T) {
	f := newFixture(t)
	stopped := f.stopped()
	old := stopped.Lease.Token
	l := f.lifecycle()
	if _, err := l.Checkpoint(context.Background(), "card", old); !errors.Is(err, model.ErrDirty) {
		t.Fatal("stray file did not refuse checkpoint", err)
	}
	if _, err := f.run(old, "/bin/sh", "-c", "rm stray.txt"); err == nil {
		t.Fatal("stopped owner launched a payload")
	}
	renewed, err := workspacerenew.Manager{Prep: f.manager}.Renew("card", 0)
	if err != nil {
		t.Fatal(err)
	}
	next := renewed.Lease
	if next.Token == old || next.Token == "" || next.Holder != "writer" || next.StopRequested || next.Continuation == "" {
		t.Fatalf("renewal did not issue a live successor: %+v", next)
	}
	if !reflect.DeepEqual(next.Execution, stopped.Lease.Execution) || !reflect.DeepEqual(next.RetainedRuns, stopped.Lease.RetainedRuns) {
		t.Fatal("renewal dropped the stopped owner's retained runs")
	}
	if !reflect.DeepEqual(f.read().Lease, next) {
		t.Fatal("returned lease differs from the saved one")
	}
	// The old token is retired for every verb.
	if _, err := f.run(old, "/bin/sh", "-c", "true"); err == nil {
		t.Fatal("retired token launched a payload")
	}
	if _, err := l.Release(context.Background(), "card", old); err == nil {
		t.Fatal("retired token released the workspace")
	}
	if _, err := f.manager.Stop("card", old, false); err == nil {
		t.Fatal("retired token stopped the successor")
	}
	out, err := f.run(next.Token, "/bin/sh", "-c", "rm stray.txt")
	if err != nil || out.ExitCode != 0 {
		t.Fatal("successor could not run the fix", err)
	}
	// What the run verb's default --checkpoint does on exit zero.
	if _, err := l.Checkpoint(context.Background(), "card", next.Token); err != nil {
		t.Fatal(err)
	}
	released, err := l.Release(context.Background(), "card", next.Token)
	if err != nil || released.Lease != nil {
		t.Fatal("successor could not release", err)
	}
	if _, err := os.Stat(filepath.Join(released.Path, "stray.txt")); !os.IsNotExist(err) {
		t.Fatal("stray file survived", err)
	}
}

func TestRenewRefusesNonRootAndBusyOperationWithoutEffect(t *testing.T) {
	f := newFixture(t)
	stopped := f.stopped()
	for _, uid := range []uint32{1, 501, 65534} {
		if _, err := (workspacerenew.Manager{Prep: f.manager}).Renew("card", uid); err != workspacerenew.ErrRoot {
			t.Fatal("non-root renewal", uid, err)
		}
	}
	if !reflect.DeepEqual(f.read(), stopped) {
		t.Fatal("refused renewal changed the workspace")
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 0, RequestID: "pending-run", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "run", Epoch: strings.Repeat("b", 32)}
	if err := f.update(func(_ benchstate.Database, tx *sql.Tx) error {
		return operationcontrol.Begin(tx, i, stopped.Workspace.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := (workspacerenew.Manager{Prep: f.manager}).Renew("card", 0); err != workspacerenew.ErrBusy {
		t.Fatal("renewal under an active operation", err)
	}
	if !reflect.DeepEqual(f.read(), stopped) {
		t.Fatal("refused renewal changed the workspace")
	}
}

func TestRenewRefusesALiveExecution(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	f.backend.onStart = func() { close(started) }
	done := make(chan struct{})
	go func() { _, _ = f.run(f.token, "/bin/sh", "-c", "exec sleep 30"); close(done) }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("payload did not start")
	}
	// A stop recorded before its fence: the execution is still running.
	if err := f.update(func(rows benchstate.Database, _ *sql.Tx) error {
		rows["card"].Lease.StopRequested, rows["card"].Lease.Continuation = true, ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	live := f.read()
	if live.Lease.Execution == nil || live.Lease.Execution.Phase != "running" {
		t.Fatal("fixture execution is not live")
	}
	fences := f.backend.fences
	if _, err := (workspacerenew.Manager{Prep: f.manager}).Renew("card", 0); err != workspacerenew.ErrLive {
		t.Fatal("renewal of a live execution", err)
	}
	if f.backend.fences != fences || !reflect.DeepEqual(f.read().Lease, live.Lease) {
		t.Fatal("refused renewal acted on the live execution")
	}
	if _, err := f.manager.Stop("card", "", true); err != nil {
		t.Fatal(err)
	}
	<-done
}

// Each refusal is a closed static error, decided before any write.
func TestRenewRefusalsAreStatic(t *testing.T) {
	f := newFixture(t)
	stopped := f.stopped()
	for name, tc := range map[string]struct {
		change func(benchstate.Database)
		want   error
	}{
		"unknown":      {func(rows benchstate.Database) { delete(rows, "card") }, workspacerenew.ErrUnknown},
		"scalar":       {func(rows benchstate.Database) { rows["card"].Workspace = nil }, workspacerenew.ErrUnknown},
		"released":     {func(rows benchstate.Database) { rows["card"].Lease = nil }, workspacerenew.ErrNotStopped},
		"not-stopped":  {func(rows benchstate.Database) { rows["card"].Lease.StopRequested = false }, workspacerenew.ErrNotStopped},
		"continuation": {func(rows benchstate.Database) { rows["card"].Lease.Continuation = strings.Repeat("c", 32) }, workspacerenew.ErrNotStopped},
		"initializing": {func(rows benchstate.Database) { rows["card"].Workspace.Phase = "initializing" }, workspacerenew.ErrState},
		"archived":     {func(rows benchstate.Database) { rows["card"].Workspace.Archive = &model.WorkspaceArchive{} }, workspacerenew.ErrState},
		"archived-at":  {func(rows benchstate.Database) { rows["card"].ArchivedAt = "2026-01-01T00:00:00Z" }, workspacerenew.ErrState},
		"transfer":     {func(rows benchstate.Database) { rows["card"].Workspace.Transfer = &model.WorkspaceTransfer{} }, workspacerenew.ErrState},
		"repair": {func(rows benchstate.Database) {
			rows["card"].Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{}
		}, workspacerenew.ErrState},
		"pending":        {func(rows benchstate.Database) { rows["card"].Workspace.Pending = &model.WorkspaceCheckpoint{} }, workspacerenew.ErrState},
		"starting":       {func(rows benchstate.Database) { rows["card"].Lease.Execution.Phase = "starting" }, workspacerenew.ErrLive},
		"running":        {func(rows benchstate.Database) { rows["card"].Lease.Execution.Phase = "running" }, workspacerenew.ErrLive},
		"initialization": {func(rows benchstate.Database) { rows["card"].Lease.Holder = "initialization" }, workspacerenew.ErrState},
	} {
		t.Run(name, func(t *testing.T) {
			p := f.manager
			p.Read = func(fn func(benchstate.Database) error) error {
				rows := benchstate.Database{"card": workspace.Clone(stopped)}
				tc.change(rows)
				return fn(rows)
			}
			p.Update = func(func(benchstate.Database, *sql.Tx) error) error {
				t.Fatal("refusal reached a write")
				return nil
			}
			if _, err := (workspacerenew.Manager{Prep: p}).Renew("card", 0); err != tc.want {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// The durable transition admits only root's successor: a new token and fresh
// continuation for the same holder, over a stopped owner, keeping its runs.
func TestRenewalTransitionNeverRevivesAStoppedToken(t *testing.T) {
	f := newFixture(t)
	stopped := f.stopped()
	successor := func(b *model.Bench) {
		b.Lease.Token, b.Lease.Continuation, b.Lease.StopRequested = strings.Repeat("d", 32), strings.Repeat("e", 32), false
	}
	check := func(name string, base *model.Bench, change func(*model.Bench), valid bool) {
		t.Helper()
		rows := benchstate.Database{"card": workspace.Clone(base)}
		before := benchstate.Capture(rows)
		change(rows["card"])
		err := benchstate.ValidateTransition(before, rows)
		if valid && err == nil {
			err = benchstate.Validate(rows)
		}
		if (err == nil) != valid {
			t.Errorf("%s: valid=%v, got %v", name, valid, err)
		}
	}
	check("successor", stopped, successor, true)
	check("clear stop in place", stopped, func(b *model.Bench) {
		b.Lease.StopRequested, b.Lease.Continuation = false, strings.Repeat("e", 32)
	}, false)
	check("successor still stopped", stopped, func(b *model.Bench) { successor(b); b.Lease.StopRequested, b.Lease.Continuation = true, "" }, false)
	check("successor without continuation", stopped, func(b *model.Bench) { successor(b); b.Lease.Continuation = "" }, false)
	check("other holder", stopped, func(b *model.Bench) { successor(b); b.Lease.Holder = "other" }, false)
	check("dropped runs", stopped, func(b *model.Bench) { successor(b); b.Lease.RetainedRuns = nil }, false)
	check("dropped execution", stopped, func(b *model.Bench) { successor(b); b.Lease.Execution = nil }, false)
	check("pending checkpoint", stopped, func(b *model.Bench) { successor(b); b.Workspace.Pending = &model.WorkspaceCheckpoint{} }, false)
	live := workspace.Clone(stopped)
	live.Lease.StopRequested, live.Lease.Continuation = false, strings.Repeat("f", 32)
	check("live owner replaced", live, successor, false)
	running := workspace.Clone(stopped)
	running.Lease.Execution.Phase = "running"
	check("running owner replaced", running, successor, false)
}

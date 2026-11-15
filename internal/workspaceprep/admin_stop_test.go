package workspaceprep_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

func TestAdminStopPayloadStillSealsActualOutcomeAndAllowsRepairRelease(t *testing.T) {
	f := newFixture(t, 2, nil)
	f.ready()
	if _, err := f.prepare(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	for _, member := range f.initial.Workspace.Members {
		git(t, member.Path, "add", ".")
		git(t, member.Path, "commit", "-m", "preserve operational preparation output")
	}
	started := make(chan struct{})
	f.backend.onStart = func() { close(started) }
	done := make(chan error, 1)
	go func() {
		result, err := f.run("r00", "/bin/sh", "-c", "printf captured; exec sleep 30")
		if result == nil || result.Execution == nil || result.Execution.Phase != "stopped" {
			done <- context.Canceled
			return
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("payload did not start")
	}
	if _, err := f.manager.Stop("card", "", true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || err == context.Canceled {
			t.Fatal("stopped payload lost its actual stopped result", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stopped payload did not finish capture")
	}
	current := f.read()
	if !current.Lease.StopRequested || current.Lease.Continuation != "" || current.Lease.Execution.Phase != "stopped" {
		t.Fatal("runner acknowledgement revived continuation")
	}
	if err := f.update(func(_ benchstate.Database, tx *sql.Tx) error {
		r, err := history.Get(tx, current.Lease.Execution.ID)
		if err == nil && (r.File == nil || r.ExitCode == nil || r.SealedAt.IsZero() || !r.ReleasedAt.IsZero()) {
			t.Fatal("admin stop left an unsealed or prematurely released history")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	l := f.lifecycle()
	if _, err := l.Checkpoint(context.Background(), "card", current.Lease.Token); err != nil {
		t.Fatal(err)
	}
	if released, err := l.Release(context.Background(), "card", current.Lease.Token); err != nil || released.Lease != nil {
		t.Fatal("sealed administrative stop cannot be explicitly reconciled", err)
	}
}

func TestAdminStopAfterSuccessfulSetupSealsButNeverRecordsPreparation(t *testing.T) {
	f := newFixture(t, 2, func(int) string { return "[setup]\nonce = 'true'\n" })
	gate := f.manager.Gate
	stopped := false
	f.manager.Gate = func(key string, fn func() error) error {
		b := f.read()
		if !stopped && b.Lease.Execution != nil && b.Lease.Execution.Phase == "running" {
			// This is the completion boundary, after Wait returned exit zero.
			stopped = true
			if _, err := f.manager.Stop(key, "", true); err != nil {
				return err
			}
		}
		return gate(key, fn)
	}
	if _, err := f.prepare(context.Background(), ""); err == nil {
		t.Fatal("stopped setup reported preparation success")
	}
	b := f.read()
	if !stopped || len(b.Workspace.Preparation) != 0 || b.Lease.Continuation != "" || !b.Lease.StopRequested || b.Lease.Execution.Phase != "stopped" {
		t.Fatal("stopped successful command granted preparation or continuation")
	}
	if _, starts := f.backend.counts(); starts != 1 {
		t.Fatal("stopped setup launched a later member")
	}
	if err := f.update(func(_ benchstate.Database, tx *sql.Tx) error {
		r, err := history.Get(tx, b.Lease.Execution.ID)
		if err != nil {
			return err
		}
		if r.File == nil || r.ExitCode == nil || *r.ExitCode != 0 {
			t.Fatal("actual successful command outcome was not sealed")
		}
		complete, err := setup.Completed(tx, "workspace:"+b.Workspace.ID+":r00", b.Workspace.Stage.Digest)
		if complete {
			t.Fatal("administratively stopped stage created a once receipt")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAdminStopAfterPayloadExitZeroRetainsSealedOwner(t *testing.T) {
	f := newFixture(t, 1, nil)
	f.ready()
	if _, err := f.prepare(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	gate := f.manager.Gate
	stopped := false
	f.manager.Gate = func(key string, fn func() error) error {
		b := f.read()
		if !stopped && b.Lease.Execution != nil && b.Lease.Execution.Phase == "running" {
			stopped = true
			if _, err := f.manager.Stop(key, "", true); err != nil {
				return err
			}
		}
		return gate(key, fn)
	}
	out, err := f.run("r00", "/bin/sh", "-c", "exit 0")
	if err == nil || !stopped || out == nil || out.ExitCode != 0 || out.Execution.Phase != "stopped" || out.Bench.Lease == nil || !out.Bench.Lease.StopRequested || out.Bench.Lease.Continuation != "" {
		t.Fatal("successful command overrode administrative stop or lost actual outcome", err)
	}
	if err := f.update(func(_ benchstate.Database, tx *sql.Tx) error {
		r, err := history.Get(tx, out.Execution.ID)
		if err == nil && (r.File == nil || r.ExitCode == nil || *r.ExitCode != 0 || !r.ReleasedAt.IsZero()) {
			t.Fatal("successful stopped payload failed to retain sealed history")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

package workspaceprep_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

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
	if _, err := f.prepare(context.Background(), ""); err != nil {
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
func (f *fixture) run(selector string, args ...string) (*workspaceprep.RunResult, error) {
	return f.manager.Run(context.Background(), "card", f.initial.Lease.Token, selector, args, false)
}

func TestOperationalPayloadUsesWholePreparationAndRegisteredCwd(t *testing.T) {
	for _, selector := range []string{"", "r01"} {
		t.Run(selector, func(t *testing.T) {
			f := newFixture(t, 2, nil)
			f.ready()
			checkpoint := workspace.Clone(f.initial).Workspace.Checkpoint
			if len(f.initial.Workspace.Preparation) != 0 {
				t.Fatal("acquisition inherited released preparation proofs")
			}
			result, err := f.run(selector, "/bin/sh", "-c", "pwd; printf payload")
			if err != nil || result == nil || result.ExitCode != 0 || result.Bench.Workspace.Phase != "ready" || result.Bench.Lease == nil || result.Execution.Phase != "stopped" {
				t.Fatalf("operational payload: %+v %v", result, err)
			}
			if !reflect.DeepEqual(checkpoint, result.Bench.Workspace.Checkpoint) {
				t.Fatal("payload/preparation rewrote sealed checkpoint")
			}
			want := result.Bench.Path
			if selector != "" {
				want = result.Bench.Workspace.Members[1].Path
			}
			want, _ = filepath.EvalSymlinks(want)
			raw, err := os.ReadFile(result.Execution.Log)
			if err != nil || strings.TrimSpace(string(raw)) != want+"\npayload" {
				t.Fatalf("wrong payload cwd: %q %v", raw, err)
			}
			if len(result.Bench.Workspace.Preparation) != 2 || len(result.Bench.Lease.RetainedRuns) != 2 || result.Bench.Workspace.Stage != nil {
				t.Fatal("payload reused setup stage or lost prior logs")
			}
			for i := 0; i < 2; i++ {
				if f.count(i, "once-count") != 1 || f.count(i, "each-count") != 2 {
					t.Fatal("once/each acquisition semantics changed")
				}
			}
			for _, proof := range result.Bench.Workspace.Preparation {
				if proof.RunID == result.Execution.ID {
					t.Fatal("payload manufactured setup proof")
				}
			}
			previousPayload := result.Execution.ID
			result, err = f.run(selector, "/bin/sh", "-c", "exit 0")
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Bench.Lease.RetainedRuns) != 3 || result.Bench.Lease.RetainedRuns[2] != previousPayload {
				t.Fatal("second payload lost prior held stage evidence")
			}
			for i := 0; i < 2; i++ {
				if f.count(i, "each-count") != 2 {
					t.Fatal("same held lease unnecessarily repeated current preparation")
				}
			}
		})
	}
}

func TestReleasedInitializationCannotLaunchOperationalPayload(t *testing.T) {
	f := newFixture(t, 2, nil)
	oldToken := f.ready()
	beforeCreates, beforeStarts := f.backend.counts()
	if _, err := f.manager.Run(context.Background(), "card", oldToken, "r00", []string{"/bin/sh", "-c", "echo forbidden > forbidden"}, false); err == nil {
		t.Fatal("released initialization token launched payload")
	}
	if creates, starts := f.backend.counts(); creates != beforeCreates || starts != beforeStarts {
		t.Fatal("released initialization owner created a scope")
	}
	if _, err := os.Lstat(filepath.Join(f.initial.Workspace.Members[0].Path, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("released initialization owner produced output")
	}
}

func TestPayloadMayEditPreparationInputsWithoutLosingStoppedOwnership(t *testing.T) {
	f := newFixture(t, 2, nil)
	f.ready()
	result, err := f.run("r00", "/bin/sh", "-c", "printf '{\"edited\":true}\\n' > package.json; git -c user.name=fixture -c user.email=fixture@example.invalid add package.json; git -c user.name=fixture -c user.email=fixture@example.invalid commit -m 'payload edit'")
	if err != nil || result == nil || result.ExitCode != 0 || result.Execution.Phase != "stopped" || result.Bench.Lease == nil {
		t.Fatalf("legitimate input edit rejected after payload: %+v %v", result, err)
	}
	if err := workspaceprep.CheckPrepared(result.Bench); err == nil {
		t.Fatal("old preparation fingerprints accidentally match changed input")
	}
	// A later payload must refresh only the stale member, under the same lease.
	if _, err := f.run("r00", "/bin/sh", "-c", "exit 0"); err != nil {
		t.Fatal(err)
	}
	if f.count(0, "once-count") != 2 || f.count(0, "each-count") != 3 || f.count(1, "each-count") != 2 {
		t.Fatal("later payload did not reprepare changed member correctly")
	}
}

func TestOperationalPayloadFailuresRetainHeldSealedOutcome(t *testing.T) {
	f := newFixture(t, 2, nil)
	f.ready()
	result, err := f.run("r01", "/bin/sh", "-c", "printf failed; exit 7")
	if err == nil || result == nil || result.ExitCode != 7 || result.Execution.Phase != "stopped" || result.Bench.Lease == nil {
		t.Fatalf("failed payload ownership: %+v %v", result, err)
	}
	if err := f.manager.Update(func(_ benchstate.Database, tx *sql.Tx) error {
		r, err := history.Get(tx, result.Execution.ID)
		if err != nil {
			return err
		}
		if r.File == nil || r.ExitCode == nil || *r.ExitCode != 7 || !r.ReleasedAt.IsZero() {
			t.Error("failed payload was not sealed and retained")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run("r01", "/bin/sh", "-c", "exit 0"); err != nil {
		t.Fatal("sealed failed payload could not be retained for explicit continuation", err)
	}
}

func TestOperationalStopPreventsAnyLaterPayload(t *testing.T) {
	f := newFixture(t, 2, nil)
	f.ready()
	if _, err := f.manager.Stop("card", f.initial.Lease.Token, false); err != nil {
		t.Fatal(err)
	}
	beforeCreates, beforeStarts := f.backend.counts()
	if _, err := f.run("r00", "/bin/sh", "-c", "echo forbidden > forbidden"); err == nil {
		t.Fatal("stopped lease resumed payload")
	}
	if creates, starts := f.backend.counts(); creates != beforeCreates || starts != beforeStarts {
		t.Fatal("stopped lease allocated a stage")
	}
}

func TestPayloadSealFailureRetainsExecutionAndPreventsReplacement(t *testing.T) {
	f := newFixture(t, 2, nil)
	f.ready()
	if _, err := f.prepare(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	sealErr := errors.New("injected payload seal save failure")
	f.manager.Seal = func(string, *output.Capture, *int, bool) error { return sealErr }
	result, err := f.run("r00", "/bin/sh", "-c", "exit 0")
	if !errors.Is(err, sealErr) || result == nil || result.Bench.Lease == nil || result.Execution.Phase != "stopped" {
		t.Fatalf("seal failure lost stopped ownership: %+v %v", result, err)
	}
	beforeCreates, beforeStarts := f.backend.counts()
	if _, err := f.run("r00", "/bin/sh", "-c", "exit 0"); err == nil {
		t.Fatal("unsealed held execution was replaced")
	}
	if creates, starts := f.backend.counts(); creates != beforeCreates || starts != beforeStarts {
		t.Fatal("unsealed prior payload allowed new scope")
	}
}

func TestOperationalPayloadCancellationRevokesContinuation(t *testing.T) {
	f := newFixture(t, 2, nil)
	f.ready()
	if _, err := f.prepare(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	f.backend.onStart = func() { close(started) }
	done := make(chan error, 1)
	go func() {
		_, err := f.manager.Run(ctx, "card", f.initial.Lease.Token, "r00", []string{"/bin/sh", "-c", "exec sleep 30"}, false)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("payload did not launch")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled payload reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("payload did not finish cancellation")
	}
	b := f.read()
	if !b.Lease.StopRequested || b.Lease.Continuation != "" {
		t.Fatal("canceled payload returned before revoking continuation")
	}
	if _, err := f.run("r01", "/bin/sh", "-c", "exit 0"); err == nil {
		t.Fatal("immediate retry continued canceled lease")
	}
}

func TestPayloadRetainedBudgetRefusesReplacementWithoutLosingEvidence(t *testing.T) {
	f := newFixture(t, 2, nil)
	f.ready()
	if _, err := f.prepare(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if err := f.update(func(rows benchstate.Database, tx *sql.Tx) error {
		b := rows["card"]
		for len(b.Lease.RetainedRuns) < model.WorkspaceRetainedRuns {
			id := fmt.Sprintf("%032x", len(b.Lease.RetainedRuns)+1)
			path := filepath.Join(f.store.State, "run-"+id+".log")
			if err := os.WriteFile(path, []byte("preserved history"), 0600); err != nil {
				return err
			}
			if err := history.Start(tx, history.Record{ID: id, Key: "card", Holder: b.Lease.Holder, StartedAt: time.Now().UTC()}); err != nil {
				return err
			}
			entry, err := retention.Snapshot(f.store.State, id)
			if err != nil {
				return err
			}
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			exit := 0
			if err := history.Seal(tx, id, entry, info.Size(), &exit, false); err != nil {
				return err
			}
			b.Lease.RetainedRuns = append(b.Lease.RetainedRuns, id)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first, err := f.run("r00", "/bin/sh", "-c", "exit 0")
	if err != nil {
		t.Fatal(err)
	}
	beforeCreates, beforeStarts := f.backend.counts()
	second, err := f.run("r00", "/bin/sh", "-c", "exit 0")
	if err == nil || second == nil || second.Bench.Lease == nil || second.Bench.Lease.Execution.ID != first.Execution.ID || len(second.Bench.Lease.RetainedRuns) != model.WorkspaceRetainedRuns {
		t.Fatalf("retention budget replaced/lost held logs: %+v %v", second, err)
	}
	if creates, starts := f.backend.counts(); creates != beforeCreates || starts != beforeStarts {
		t.Fatal("exhausted retained budget created a new scope")
	}
}

func TestInvalidPayloadSelectorAndArgumentsHaveNoPreparationEffects(t *testing.T) {
	f := newFixture(t, 2, nil)
	f.ready()
	before, err := os.ReadFile(filepath.Join(f.store.State, "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	creates, starts := f.backend.counts()
	for _, request := range []struct {
		selector string
		args     []string
	}{{"missing", []string{"true"}}, {"r00", nil}, {"r00", []string{""}}, {"r00", []string{"true", strings.Repeat("x", 8193)}}} {
		if _, err := f.manager.Run(context.Background(), "card", f.initial.Lease.Token, request.selector, request.args, false); err == nil {
			t.Fatal("invalid payload request accepted")
		}
	}
	after, err := os.ReadFile(filepath.Join(f.store.State, "bench.db"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid payload request changed state")
	}
	if c, s := f.backend.counts(); c != creates || s != starts {
		t.Fatal("invalid payload request created a preparation stage")
	}
	for i := 0; i < 2; i++ {
		if f.count(i, "each-count") != 1 {
			t.Fatal("invalid payload request ran acquisition preparation")
		}
	}
}

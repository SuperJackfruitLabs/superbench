package workspacerepair_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func TestClaimAdmissionRefusalPreservesRevokedOwnerAndJournal(t *testing.T) {
	f := newFixture(t, nil)
	f.stop()
	p := f.inspect()
	before := f.stateBytes()
	admissionErr := errors.New("fixture admission refused")
	f.m.Prep.Admit = func() error { return admissionErr }
	if out, err := f.m.Claim(context.Background(), *p, "repair1"); !errors.Is(err, admissionErr) || out != nil {
		t.Fatal("admission failure granted repair authority", err)
	}
	if !bytes.Equal(before, f.stateBytes()) || !f.row().Lease.StopRequested || f.backend.count() != 0 {
		t.Fatal("admission refusal changed journal/owner or ran setup")
	}
}

func TestMissingAdmissionAuthorityRefusesWithoutPanicOrMutation(t *testing.T) {
	f := newFixture(t, nil)
	f.stop()
	p := f.inspect()
	before := f.stateBytes()
	f.m.Prep.Admit = nil
	if out, err := f.m.Claim(context.Background(), *p, "repair1"); err == nil || out != nil {
		t.Fatal("missing admission authority accepted claim")
	}
	if out, err := f.m.Continue(context.Background(), "card", "repair1", f.initial.Lease.Token); err == nil || out != nil {
		t.Fatal("missing admission authority accepted continuation")
	}
	if !bytes.Equal(before, f.stateBytes()) {
		t.Fatal("malformed manager changed private state")
	}
}

func TestContinueCancellationCoversInitialAuthorizationAndFinalView(t *testing.T) {
	for _, boundary := range []string{"authorization", "final-view"} {
		t.Run(boundary, func(t *testing.T) {
			f := newFixture(t, nil)
			f.stop()
			owner := f.claim(f.inspect(), "repair1")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			once := sync.Once{}
			if boundary == "authorization" {
				update := f.m.Prep.Update
				f.m.Prep.Update = func(change workspace.Change) error {
					return update(func(rows benchstate.Database, tx *sql.Tx) error { err := change(rows, tx); once.Do(cancel); return err })
				}
			} else {
				atFinalView := false
				f.m.Lifecycle.Within = func(func(workspace.Update) error) error {
					atFinalView = true
					return errors.New("fixture finalization boundary")
				}
				read := f.m.Prep.Read
				f.m.Prep.Read = func(fn func(benchstate.Database) error) error {
					if atFinalView {
						once.Do(cancel)
					}
					return read(fn)
				}
			}
			out, err := f.m.Continue(ctx, "card", "repair1", owner.GrantToken)
			if !errors.Is(err, context.Canceled) || out == nil || out.GrantToken != "" || !out.StopRequested {
				t.Fatal("canceled continuation returned runnable authority", err)
			}
			b := f.row()
			if b.Lease == nil || b.Lease.Continuation != "" || !b.Lease.StopRequested || f.backend.count() != 0 {
				t.Fatal("cancellation discarded owner, kept continuation or ran effects")
			}
			if _, err := f.m.Continue(context.Background(), "card", "repair1", owner.GrantToken); err == nil {
				t.Fatal("canceled repair restarted through same epoch")
			}
		})
	}
}

func TestContinueCancellationDuringHookStopsBeforeLaterMember(t *testing.T) {
	f := newFixture(t, func(i int) string {
		if i == 0 {
			return "[setup]\nonce = 'exec sleep 30'\n"
		}
		return hooks(i)
	})
	f.stop()
	owner := f.claim(f.inspect(), "repair1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	f.backend.onStart = func() { close(started) }
	type completion struct {
		stopped bool
		err     error
	}
	done := make(chan completion, 1)
	go func() {
		out, err := f.m.Continue(ctx, "card", "repair1", owner.GrantToken)
		done <- completion{out != nil && out.StopRequested && out.GrantToken == "", err}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("repair setup did not start")
	}
	cancel()
	select {
	case result := <-done:
		if !result.stopped || !errors.Is(result.err, context.Canceled) {
			t.Fatal("canceled hook lost stopped repair owner", result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled repair did not stop")
	}
	b := f.row()
	if b.Lease == nil || b.Lease.Continuation != "" || len(b.Workspace.Preparation) != 0 || f.backend.count() != 1 || f.count(1, "once") != 0 {
		t.Fatal("canceled repair created proof or ran later setup")
	}
}

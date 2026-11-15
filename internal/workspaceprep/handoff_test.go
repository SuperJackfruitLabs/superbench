package workspaceprep_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

func TestAcquiredHandoffRefusesLaterHookInvalidatingEarlierProof(t *testing.T) {
	for _, phase := range []string{"initialization", "operational"} {
		t.Run(phase, func(t *testing.T) { testInvalidatedHandoff(t, phase == "operational") })
	}
}

func testInvalidatedHandoff(t *testing.T, operational bool) {
	marker := filepath.Join(t.TempDir(), "change-earlier-member")
	f := newFixture(t, 2, func(i int) string {
		if i == 1 {
			return "[setup]\neach = 'if test -f " + marker + "; then printf changed > ../r00/package.json; fi'\n"
		}
		return "[setup]\neach = 'true'\n"
	})
	if operational {
		if _, err := f.prepare(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
		l := f.lifecycle()
		if _, err := l.FinishInitialization(context.Background(), "card", f.initial.Lease.Token); err != nil {
			t.Fatal(err)
		}
		acquired, err := l.Acquire(context.Background(), "card", "next-owner")
		if err != nil {
			t.Fatal(err)
		}
		f.initial = acquired
	}
	if err := os.WriteFile(marker, []byte("enable"), 0600); err != nil {
		t.Fatal(err)
	}
	sealed := 0
	f.afterSeal = func() {
		sealed++
	}
	out, err := f.manager.PrepareAcquired(context.Background(), f.initial, "", false)
	if !errors.Is(err, workspaceprep.ErrPreparation) || out == nil || out.Lease == nil || out.Lease.Token != f.initial.Lease.Token {
		t.Fatal("invalidated earlier proof became a successful handoff or lost owner", err)
	}
	wantStarts := 2
	if operational {
		wantStarts = 4
	}
	if _, starts := f.backend.counts(); starts != wantStarts || sealed != 2 {
		t.Fatal("fixture did not exercise both fenced/sealed members", starts, sealed)
	}
	current := f.read()
	if len(current.Workspace.Preparation) != 2 || !operational && (current.Workspace.Phase != "initializing" || current.Workspace.Checkpoint != nil) || operational && (current.Workspace.Phase != "ready" || current.Workspace.Checkpoint == nil || current.Lease.Holder != "next-owner") {
		t.Fatal("failed handoff fabricated readiness or discarded durable evidence")
	}
}

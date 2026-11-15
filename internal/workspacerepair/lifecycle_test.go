package workspacerepair_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceops"
)

func TestExplicitRepairClaimContinueAndCompletedCapabilityBoundary(t *testing.T) {
	f := newFixture(t, nil)
	oldToken := f.initial.Lease.Token
	f.stop()
	p := f.inspect()
	before := f.stateBytes()
	if len(p.Members) != 2 || p.Members[0].Action != "prepare" {
		t.Fatal("unexpected cold repair decisions")
	}
	r := f.claim(p, "repair1")
	if r.GrantToken == oldToken || r.RootPath == "" || r.Phase != "initializing" {
		t.Fatal("repair failed to replace revoked owner")
	}
	replay, err := f.m.Claim(context.Background(), *p, "repair1")
	must(t, err)
	if replay.GrantToken != r.GrantToken {
		t.Fatal("lost claim reply changed the exact owner")
	}
	claimed := f.stateBytes()
	if bytes.Equal(before, claimed) {
		t.Fatal("claim did not durably journal authority")
	}
	for name, call := range map[string]func() error{
		"stage": func() error {
			_, err := f.store.StageWorkspaceContext(context.Background(), f.initial.Workspace.CreationID, f.spec)
			return err
		},
		"warm-create": func() error {
			_, err := f.store.InitializeWorkspaceContext(context.Background(), f.initial.Workspace.CreationID, f.spec)
			return err
		},
		"recovery-stage": func() error {
			_, err := f.store.StageWorkspaceRecoveryContext(context.Background(), f.initial.Workspace.CreationID, workspace.RecoverySpec{Key: "card", Repositories: f.spec.Repositories, Source: workspace.RecoverySource{Repository: f.spec.Repositories[0].Repository, BenchID: strings.Repeat("8", 32), Commit: f.initial.Workspace.Members[0].Source, ManifestSHA256: strings.Repeat("9", 64)}})
			return err
		},
		"raw-finish": func() error {
			_, err := f.store.FinishWorkspaceInitializationContext(context.Background(), "card", r.GrantToken)
			return err
		},
		"public-create": func() error {
			out, err := (workspaceops.Manager{Store: f.store}).Execute(context.Background(), f.request, 42)
			if out != nil && out.GrantToken != "" {
				t.Fatal("old create request obtained repair capability")
			}
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("ordinary retry bypassed explicit repair epoch")
			}
			if !bytes.Equal(claimed, f.stateBytes()) {
				t.Fatal("refused retry changed repair state")
			}
		})
	}
	if _, err := f.m.Continue(context.Background(), "card", "repair1", oldToken); err == nil {
		t.Fatal("old initialization capability continued repair")
	}
	out, err := f.m.Continue(context.Background(), "card", "repair1", r.GrantToken)
	must(t, err)
	if out == nil || out.Phase != "ready" || out.GrantToken != "" || out.RootPath == "" || f.row().Lease != nil {
		t.Fatal("repair did not publish whole readiness without reexporting capability")
	}
	ready := f.stateBytes()
	for i := 0; i < 2; i++ {
		again, err := f.m.Continue(context.Background(), "card", "repair1", r.GrantToken)
		must(t, err)
		if again.Phase != "ready" || again.GrantToken != "" || !bytes.Equal(ready, f.stateBytes()) {
			t.Fatal("completed exact continuation rewrote evidence or exported capability")
		}
	}
	if out, err := f.m.Continue(context.Background(), "card", "repair1", strings.Repeat("f", 48)); err == nil || out != nil {
		t.Fatal("wrong capability observed completed repair root")
	}
	owner, err := f.store.AcquireWorkspaceOwnerContext(context.Background(), "card", "later-owner")
	must(t, err)
	later := f.stateBytes()
	if out, err := f.m.Continue(context.Background(), "card", "repair1", r.GrantToken); err == nil || out != nil {
		t.Fatal("historical continuation leaked later owner")
	}
	if out, err := f.m.Claim(context.Background(), *p, "repair1"); err == nil || out != nil {
		t.Fatal("historical approval leaked later owner")
	}
	if !bytes.Equal(later, f.stateBytes()) || f.row().Lease.Token != owner.Lease.Token {
		t.Fatal("historical replay changed later owner")
	}
}

func TestConcurrentRepairClaimsChooseExactlyOneEpoch(t *testing.T) {
	f := newFixture(t, nil)
	f.stop()
	p := f.inspect()
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan bool, 2)
	for _, id := range []string{"repair1", "repair2"} {
		go func(id string) {
			defer wg.Done()
			r, err := f.m.Claim(context.Background(), *p, id)
			results <- err == nil && r != nil && r.GrantToken != ""
		}(id)
	}
	wg.Wait()
	close(results)
	wins := 0
	for ok := range results {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatal("concurrent approvals created multiple or zero owners", wins)
	}
}

func TestClaimCancellationDuringFinalViewRetainsStoppedOwnerWithoutGrant(t *testing.T) {
	f := newFixture(t, func(int) string { return "[setup]\nonce = 'printf once >> .once-output'\n" })
	f.stop()
	p := f.inspect()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	read := f.m.Prep.Read
	once := sync.Once{}
	f.m.Prep.Read = func(fn func(benchstate.Database) error) error {
		return read(func(rows benchstate.Database) error {
			if rows["card"].Workspace.InitializationRepair != nil {
				once.Do(cancel)
			}
			return fn(rows)
		})
	}
	out, err := f.m.Claim(ctx, *p, "repair1")
	if !errors.Is(err, context.Canceled) || out == nil || out.GrantToken != "" || !out.StopRequested {
		t.Fatal("canceled final claim view exported runnable authority", err)
	}
	b := f.row()
	if b.Lease == nil || !b.Lease.StopRequested || b.Lease.Continuation != "" || f.backend.count() != 0 {
		t.Fatal("canceled claim lost ownership or launched effects")
	}
	if _, err := f.m.Continue(context.Background(), "card", "repair1", b.Lease.Token); err == nil {
		t.Fatal("stopped canceled claim continued")
	}
	if _, err := f.store.StageWorkspaceContext(context.Background(), f.initial.Workspace.CreationID, f.spec); !errors.Is(err, workspace.ErrInitializationRepair) {
		t.Fatal("ordinary staging recovered canceled repair authority", err)
	}
}

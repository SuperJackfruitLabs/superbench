package workspace

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func TestFreshStageUsesDurableOwnerAndBypassesLegacyEffects(t *testing.T) {
	f := newStaging(t)
	calls := 0
	realGit := f.manager.Git
	f.manager.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "worktree" {
			t.Fatal("fresh path selected legacy materializer")
		}
		return realGit(ctx, dir, args...)
	}
	f.manager.Fresh = func(ctx context.Context, b *model.Bench) error {
		calls++
		stored := f.read(t)
		if !f.held || stored == nil || !reflect.DeepEqual(stored, b) || b.ProjectID != 0 || b.Workspace.Phase != "materializing" || b.Lease == nil {
			t.Fatal("fresh effect preceded durable exact vector/owner")
		}
		// Synthetic callback tests staging transitions, not physical materialization.
		b.ProjectID, b.QuotaBytes, b.QuotaInodes = 10000, 1<<30, 1000
		return nil
	}
	b, err := f.manager.Stage(context.Background(), "fresh-request", f.spec)
	if err != nil || b == nil || b.Workspace.Phase != "initializing" || calls != 1 || f.allocations != 0 {
		t.Fatalf("fresh staging: %+v calls=%d legacy=%d %v", b, calls, f.allocations, err)
	}
	if !reflect.DeepEqual(f.read(t), b) {
		t.Fatal("returned uncommitted allocation")
	}
	if _, err = f.manager.Stage(context.Background(), "fresh-request", f.spec); err != nil || calls != 1 {
		t.Fatalf("initializing replay rematerialized: %v calls=%d", err, calls)
	}
}

func TestFreshStageRetainsOriginalOwnerOnFailedOrInvalidEffect(t *testing.T) {
	for _, kind := range []string{"failure", "zero-allocation", "changed-owner"} {
		t.Run(kind, func(t *testing.T) {
			f := newStaging(t)
			failure := errors.New("fresh effect failed")
			f.manager.Fresh = func(_ context.Context, b *model.Bench) error {
				if kind == "failure" {
					return failure
				}
				if kind == "zero-allocation" {
					return nil
				}
				b.ProjectID, b.QuotaBytes, b.QuotaInodes = 10000, 1<<30, 1000
				b.Lease.Token = "changed-owner"
				return nil
			}
			b, err := f.manager.Stage(context.Background(), "fresh-request", f.spec)
			if err == nil || b == nil || b.Workspace.Phase != "materializing" || b.ProjectID != 0 || f.allocations != 0 {
				t.Fatalf("failed fresh effect advanced durable state: %+v %v", b, err)
			}
			if kind == "failure" && !errors.Is(err, failure) {
				t.Fatalf("lost effect error: %v", err)
			}
			if !reflect.DeepEqual(f.read(t), b) {
				t.Fatal("failed effect exposed uncommitted record")
			}
		})
	}
}

func TestFreshStageRetriesCommittedAllocationAfterGateFailure(t *testing.T) {
	f := newStaging(t)
	calls := 0
	f.manager.Fresh = func(_ context.Context, b *model.Bench) error {
		calls++
		if calls != 1 {
			t.Fatal("retry repeated one-shot bootstrap")
		}
		b.ProjectID, b.QuotaBytes, b.QuotaInodes = 10000, 1<<30, 1000
		return nil
	}
	realWithin := f.manager.Within
	gateFailure := errors.New("interrupted after allocation commit before initialization")
	allocatedCommitted, interrupted := false, false
	f.manager.Within = func(fn func(Update) error) error {
		return realWithin(func(update Update) error {
			return fn(func(change Change) error {
				if allocatedCommitted && !interrupted {
					interrupted = true
					return gateFailure
				}
				if err := update(change); err != nil {
					return err
				}
				stored := f.read(t)
				allocatedCommitted = stored != nil && stored.ProjectID != 0 && stored.Workspace.Phase == "materializing"
				return nil
			})
		})
	}
	b, err := f.manager.Stage(context.Background(), "fresh-request", f.spec)
	if !errors.Is(err, gateFailure) || !interrupted || b == nil || b.ProjectID != 10000 || b.Workspace.Phase != "materializing" || calls != 1 {
		t.Fatalf("missed durable allocation crash window: bench=%+v calls=%d interrupted=%v err=%v", b, calls, interrupted, err)
	}
	persisted := f.read(t)
	if !reflect.DeepEqual(persisted, b) {
		t.Fatal("interruption did not retain the committed allocation")
	}
	f.manager.Within = realWithin
	retried, err := f.manager.Stage(context.Background(), "fresh-request", f.spec)
	if err != nil || retried == nil || retried.Workspace.Phase != "initializing" || calls != 1 || f.allocations != 0 {
		t.Fatalf("retry failed to resume committed fresh allocation: bench=%+v calls=%d legacy=%d err=%v", retried, calls, f.allocations, err)
	}
	expected := privateRecord(persisted)
	expected.Workspace.Phase = "initializing"
	if !reflect.DeepEqual(retried, expected) || !reflect.DeepEqual(f.read(t), expected) {
		t.Fatal("retry changed allocation, initialization owner or pinned member vector")
	}
}

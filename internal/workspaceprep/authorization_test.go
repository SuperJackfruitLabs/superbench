package workspaceprep_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func TestCancellationBetweenGrantAndPreparationPreservesStoppedOwner(t *testing.T) {
	for _, operational := range []bool{false, true} {
		t.Run(map[bool]string{false: "initialization", true: "operational"}[operational], func(t *testing.T) {
			f := newFixture(t, 2, nil)
			if operational {
				f.ready()
			}
			beforeCreates, beforeStarts := f.backend.counts()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			result, err := f.manager.PrepareAcquired(ctx, f.initial, "", false)
			if !errors.Is(err, context.Canceled) || result == nil || result.Lease == nil || result.Lease.Token != f.initial.Lease.Token || !result.Lease.StopRequested || result.Lease.Continuation != "" {
				t.Fatal("cancellation discarded or failed to stop the committed owner", err)
			}
			if creates, starts := f.backend.counts(); creates != beforeCreates || starts != beforeStarts {
				t.Fatal("canceled handoff launched preparation")
			}
			if _, err := f.prepare(context.Background(), ""); err == nil {
				t.Fatal("canceled handoff allowed an immediate continuation")
			}
		})
	}
}

func TestFinalRefreshCannotDiscloseSubsequentOwner(t *testing.T) {
	for _, payload := range []bool{false, true} {
		name := "preparation"
		if payload {
			name = "payload"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 2, nil)
			f.ready()
			if payload {
				if _, err := f.prepare(context.Background(), ""); err != nil {
					t.Fatal(err)
				}
			}
			read := f.manager.Read
			injected := false
			f.manager.Read = func(fn func(benchstate.Database) error) error {
				return read(func(rows benchstate.Database) error {
					b := rows["card"]
					complete := len(b.Workspace.Preparation) == 2
					if payload {
						complete = b.Lease.Execution != nil && b.Lease.Execution.Phase == "stopped" && b.Workspace.Stage == nil
					}
					if complete {
						// Model a later owner's committed snapshot at the final read,
						// without changing the fixture's authoritative SQLite rows.
						b = workspace.Clone(b)
						b.Lease = &model.Lease{Holder: "later-owner", Token: strings.Repeat("e", 32), Continuation: strings.Repeat("f", 32)}
						b.Workspace.Preparation = nil
						rows["card"] = b
						injected = true
					}
					return fn(rows)
				})
			}
			if payload {
				result, err := f.run("r00", "/bin/sh", "-c", "exit 0")
				if err == nil || result == nil || result.Bench != nil {
					t.Fatal("payload disclosed a subsequent owner", err)
				}
			} else if result, err := f.prepare(context.Background(), ""); err == nil || result != nil {
				t.Fatal("preparation disclosed a subsequent owner", err)
			}
			if !injected || f.read().Lease.Token != f.initial.Lease.Token {
				t.Fatal("test did not exercise a later snapshot or changed actual ownership")
			}
		})
	}
}

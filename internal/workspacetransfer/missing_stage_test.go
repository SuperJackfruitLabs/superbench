package workspacetransfer_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/transferguard"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

// These faults occur after durable source relinquishment. The injected Git
// function delegates every actual command and changes only this fixture's
// returned source directory, never Git metadata or production authority.
func TestMissingSourceReturnDuringDestinationStaging(t *testing.T) {
	for _, boundary := range []string{"recovery-selection", "first-checkout"} {
		t.Run(boundary, func(t *testing.T) {
			f, p := missingFixture(t)
			_, err := f.m.BeginMissing(context.Background(), *p)
			must(t, err)
			a, err := f.store.WorkspaceRepair()
			must(t, err)
			must(t, f.m.RelinquishMissingForTest(context.Background(), a, *p))
			source := f.row("source")
			before := f.stateBytes()
			allocations, adds := 0, 0
			returned := false
			targetRoot := filepath.Join(f.store.Data, "benches", p.TargetKey)
			sourceB := source.Workspace.Members[1].Path
			sentinel := filepath.Join(sourceB, "sentinel")
			realGit := a.Prep.Git
			managedGit := func(ctx context.Context, dir string, args ...string) (string, error) {
				out, e := realGit(ctx, dir, args...)
				if e != nil {
					return out, e
				}
				trigger := false
				if boundary == "recovery-selection" {
					// recoveryPlanAuthorized must check local original task tips after
					// recovering the exact immutable source seal, before record/allocation.
					trigger = len(args) == 3 && args[0] == "rev-parse" && args[1] == "--verify" && args[2] == "refs/heads/"+source.Workspace.Members[0].Branch
				} else if len(args) >= 3 && args[0] == "worktree" && args[1] == "add" {
					for _, arg := range args[2:] {
						if arg == filepath.Join(targetRoot, "a") || arg == filepath.Join(targetRoot, "b") {
							adds++
							trigger = arg == filepath.Join(targetRoot, "a")
						}
					}
				}
				if trigger && !returned {
					returned = true
					if e = os.MkdirAll(sourceB, 0700); e != nil {
						return "", e
					}
					if e = os.WriteFile(sentinel, []byte("returned source must survive\n"), 0600); e != nil {
						return "", e
					}
				}
				return out, nil
			}
			manager := workspace.Manager{State: f.store.State, Data: f.store.Data, Git: managedGit, Within: a.Lifecycle.Within, Admit: a.Prep.Admit, Allocate: func(*model.Bench) error { allocations++; return nil }}
			spec := workspace.Spec{TransferID: p.ID, Key: p.TargetKey, CheckpointRepository: p.CheckpointRepository, Repositories: p.Repositories, QuotaBytes: p.QuotaBytes}
			result, err := manager.Stage(context.Background(), p.CreationID, spec)
			if !returned || !errors.Is(err, transferguard.ErrGuard) {
				t.Fatal("fault did not reach guarded Stage boundary", returned, err)
			}
			if got := read(t, sentinel); string(got) != "returned source must survive\n" {
				t.Fatal("staging changed returned source bytes")
			}
			current := f.row("source")
			if current.Lease != nil || current.Workspace.Transfer.Phase != "transferred" || current.Workspace.Checkpoint.Seal != source.Workspace.Checkpoint.Seal || f.journal(p.ID).Phase != "relinquished" {
				t.Fatal("staging failure revived or advanced source authority")
			}
			if boundary == "recovery-selection" {
				if result != nil || f.row("target") != nil || allocations != 0 || adds != 0 || !bytes.Equal(before, f.stateBytes()) {
					t.Fatal("stale recovery selection persisted or allocated destination")
				}
				if _, e := os.Lstat(targetRoot); !os.IsNotExist(e) {
					t.Fatal("stale recovery selection created target root", e)
				}
			} else {
				if result == nil || result.Workspace.Phase != "materializing" || f.row("target") == nil || allocations != 1 || adds != 1 {
					t.Fatal("staging failure lost partial durable destination", allocations, adds)
				}
				first := filepath.Join(targetRoot, "a")
				if got := git(t, first, "symbolic-ref", "HEAD"); got != "refs/heads/"+source.Workspace.Members[0].Branch {
					t.Fatal("first exact checkout was not preserved")
				}
				if got := git(t, first, "rev-parse", "HEAD"); got != p.Checkpoint.Members[0].Commit {
					t.Fatal("first checkout moved away from approved source")
				}
				if _, e := os.Lstat(filepath.Join(targetRoot, "b")); !os.IsNotExist(e) {
					t.Fatal("later target member materialized despite source return", e)
				}
				registrations := git(t, source.Workspace.Members[1].Mirror, "worktree", "list", "--porcelain")
				if strings.Contains(registrations, "worktree "+filepath.Join(targetRoot, "b")+"\n") {
					t.Fatal("later target registration created despite source return")
				}
			}
		})
	}
}

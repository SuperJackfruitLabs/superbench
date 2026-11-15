package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

func availableLifecycle(t *testing.T) (*stagingFixture, Lifecycle, *model.Bench) {
	t.Helper()
	f, l, prepared := preparedLifecycle(t)
	ready, err := l.FinishInitialization(context.Background(), prepared.Key, prepared.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	l.Stopped = func(b *model.Bench) error {
		if !f.held {
			t.Fatal("ownership fence proof outside retained host lock")
		}
		if b.Lease != nil && b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped" {
			return errors.New("execution is not stopped")
		}
		return nil
	}
	l.Admit = func() error {
		if !f.held {
			t.Fatal("acquisition admission without lock")
		}
		return nil
	}
	l.Allocate = func(b *model.Bench) error {
		if !f.held || b.ProjectID != ready.ProjectID || b.QuotaBytes != ready.QuotaBytes || b.QuotaInodes != ready.QuotaInodes {
			t.Fatal("acquisition changed aggregate allocation")
		}
		return nil
	}
	return f, l, ready
}

func TestAcquireRefreshesOnlyRemoteTrackingAndPreservesDirtyLocalWork(t *testing.T) {
	f, l, ready := availableLifecycle(t)
	member := ready.Workspace.Members[0]
	git(t, member.Path, "commit", "--allow-empty", "-m", "unpublished local work")
	local := git(t, member.Path, "rev-parse", "HEAD")
	output := filepath.Join(member.Path, "untracked-user-output")
	if err := os.WriteFile(output, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, member.Repository, "commit", "--allow-empty", "-m", "new remote work")
	remote := git(t, member.Repository, "rev-parse", "HEAD")
	git(t, member.Repository, "update-ref", "refs/heads/"+member.Branch, remote)
	owned, err := l.Acquire(context.Background(), ready.Key, "run:one")
	if err != nil {
		t.Fatal(err)
	}
	if owned.Lease == nil || owned.Lease.Holder != "run:one" || owned.Lease.Token == "" || owned.Lease.Continuation == "" || owned.Lease.Execution != nil || len(owned.Lease.RetainedRuns) != 0 || len(owned.Workspace.Preparation) != 0 || owned.Workspace.Stage != nil {
		t.Fatal("acquisition inherited old preparation/ownership or omitted its private continuation")
	}
	if !reflect.DeepEqual(ready.Workspace.Checkpoint, owned.Workspace.Checkpoint) || owned.Workspace.ID != ready.Workspace.ID || owned.ProjectID != ready.ProjectID {
		t.Fatal("acquisition changed durable identity/checkpoint/allocation")
	}
	if got := git(t, member.Mirror, "rev-parse", "refs/remotes/origin/"+member.Branch); got != remote {
		t.Fatal("acquisition did not refresh task remote tracking", got)
	}
	if got := git(t, member.Path, "rev-parse", "HEAD"); got != local {
		t.Fatal("refresh moved the user's local branch", got)
	}
	if raw, err := os.ReadFile(output); err != nil || string(raw) != "preserve" {
		t.Fatal("refresh deleted dirty work", err)
	}
	if !reflect.DeepEqual(owned, f.read(t)) {
		t.Fatal("acquisition returned uncommitted ownership")
	}
	// A repeated acquisition never returns the existing holder's private token,
	// even when the caller repeats its holder label. There is no timeout takeover.
	for _, holder := range []string{"run:one", "run:two"} {
		if result, err := l.Acquire(context.Background(), ready.Key, holder); err == nil || result != nil {
			t.Fatal("busy acquisition disclosed or replaced a private owner", result, err)
		}
		if !reflect.DeepEqual(owned, f.read(t)) {
			t.Fatal("busy acquisition changed ownership")
		}
	}
}

func TestAcquireRefusesMissingOrChangedMirrorsWithoutRecloning(t *testing.T) {
	for _, kind := range []string{"missing", "origin"} {
		t.Run(kind, func(t *testing.T) {
			f, l, ready := availableLifecycle(t)
			member := ready.Workspace.Members[1]
			if kind == "missing" {
				if err := os.Rename(member.Mirror, member.Mirror+"-preserved"); err != nil {
					t.Fatal(err)
				}
			} else {
				git(t, member.Mirror, "config", "remote.origin.url", ready.Workspace.Members[0].Repository)
			}
			l.Admit = func() error { t.Fatal("invalid registered mirror passed identity preflight"); return nil }
			result, err := l.Acquire(context.Background(), ready.Key, "holder")
			if err == nil || result == nil || result.Lease != nil || !reflect.DeepEqual(f.read(t), ready) {
				t.Fatal("invalid mirror gained ownership", err)
			}
			if kind == "missing" {
				if _, err := os.Lstat(member.Mirror); !os.IsNotExist(err) {
					t.Fatal("missing registered mirror was recloned", err)
				}
			}
		})
	}
}

func TestAcquireRefreshFailureDoesNotPublishPartialOwner(t *testing.T) {
	f, l, ready := availableLifecycle(t)
	member := ready.Workspace.Members[1]
	// The mirror still has its exact registered identity, but the configured
	// transport is unavailable. Do not reselect sources or fabricate a new repo.
	if err := os.Rename(member.Repository, member.Repository+"-preserved"); err != nil {
		t.Fatal(err)
	}
	result, err := l.Acquire(context.Background(), ready.Key, "holder")
	if err == nil || result == nil || result.Lease != nil || !reflect.DeepEqual(result, ready) || !reflect.DeepEqual(f.read(t), ready) {
		t.Fatal("refresh failure leaked or committed a new lease", err)
	}
	if err := os.Rename(member.Repository+"-preserved", member.Repository); err != nil {
		t.Fatal(err)
	}
	result, err = l.Acquire(context.Background(), ready.Key, "holder")
	if err != nil || result.Lease == nil || result.Lease.Token == "" {
		t.Fatal("clean retry after transport repair failed", err)
	}
}

func TestAcquireRejectsAllocatorMutationAndRollsBackFailedOwnershipSave(t *testing.T) {
	f, l, ready := availableLifecycle(t)
	allocate := l.Allocate
	l.Allocate = func(b *model.Bench) error { b.QuotaBytes += 1024; return nil }
	if result, err := l.Acquire(context.Background(), ready.Key, "holder"); err == nil || !reflect.DeepEqual(result, ready) || !reflect.DeepEqual(f.read(t), ready) {
		t.Fatal("existing allocation changed", err)
	}
	l.Allocate = allocate
	base := l.Within
	failed := false
	l.Within = func(fn func(Update) error) error {
		return base(func(update Update) error {
			return fn(func(change Change) error {
				return update(func(rows benchstate.Database, tx *sql.Tx) error {
					if err := change(rows, tx); err != nil {
						return err
					}
					if !failed && rows[ready.Key].Lease != nil {
						failed = true
						f.failSave = true
					}
					return nil
				})
			})
		})
	}
	result, err := l.Acquire(context.Background(), ready.Key, "holder")
	if err == nil || !failed || !reflect.DeepEqual(result, ready) || !reflect.DeepEqual(f.read(t), ready) {
		t.Fatal("failed acquisition returned uncommitted token", err)
	}
	result, err = l.Acquire(context.Background(), ready.Key, "holder")
	if err != nil || result.Lease == nil {
		t.Fatal("acquisition save retry failed", err)
	}
}

func TestReleaseRequiresExactOwnerAndCleanCompleteVector(t *testing.T) {
	f, l, ready := availableLifecycle(t)
	owned, err := l.Acquire(context.Background(), ready.Key, "holder")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := l.Release(context.Background(), ready.Key, strings.Repeat("f", 32)); err == nil || result != nil {
		t.Fatal("wrong token disclosed ownership", result, err)
	}
	path := filepath.Join(owned.Workspace.Members[1].Path, "user-work")
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := l.Release(context.Background(), owned.Key, owned.Lease.Token); !errors.Is(err, model.ErrDirty) || !reflect.DeepEqual(result, owned) {
		t.Fatal("dirty second member permitted release", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	git(t, owned.Workspace.Members[1].Path, "commit", "--allow-empty", "-m", "new committed work")
	if _, err := l.Release(context.Background(), owned.Key, owned.Lease.Token); err == nil {
		t.Fatal("new HEAD released against old checkpoint")
	}
	if !reflect.DeepEqual(f.read(t), owned) {
		t.Fatal("failed release changed held state")
	}
	cp, err := l.Checkpoint(context.Background(), owned.Key, owned.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Workspace.Checkpoint.Seal == ready.Workspace.Checkpoint.Seal {
		t.Fatal("new complete vector did not create a successor generation")
	}
	// A legitimate committed config change need not rerun setup to release its
	// preserved checkpoint. Preparation is required by acquisition/run, not proof
	// that old hooks match every change made by the payload.
	l.Prepared = func(*model.Bench) error { t.Fatal("ordinary release demanded initialization setup proof"); return nil }
	released, err := l.Release(context.Background(), cp.Key, cp.Lease.Token)
	if err != nil || released.Lease != nil || released.Workspace.Phase != "ready" || !reflect.DeepEqual(released, f.read(t)) {
		t.Fatal("complete operational checkpoint did not release", err)
	}
	if _, err := l.Release(context.Background(), cp.Key, cp.Lease.Token); err == nil {
		t.Fatal("old token replay restored ownership")
	}
}

func TestReleaseRefusesPartialPublicationAndChangedRemoteEvidence(t *testing.T) {
	for _, kind := range []string{"pending", "task", "pin"} {
		t.Run(kind, func(t *testing.T) {
			f, l, ready := availableLifecycle(t)
			owned, err := l.Acquire(context.Background(), ready.Key, "holder")
			if err != nil {
				t.Fatal(err)
			}
			member := owned.Workspace.Members[1]
			if kind == "pending" {
				git(t, member.Path, "commit", "--allow-empty", "-m", "new owned work")
				rejectTask(t, member)
				owned, err = l.Checkpoint(context.Background(), owned.Key, owned.Lease.Token)
				if err == nil || owned.Workspace.Pending == nil {
					t.Fatal("partial publication fixture failed", err)
				}
			} else {
				git(t, member.Repository, "commit", "--allow-empty", "-m", "remote administrator changed evidence")
				ref := "refs/heads/" + member.Branch
				if kind == "pin" {
					ref, _ = checkpointset.PinRef(owned.Workspace.ID, owned.Workspace.Checkpoint.GenerationID, member.Name)
				}
				git(t, member.Repository, "update-ref", ref, git(t, member.Repository, "rev-parse", "HEAD"))
			}
			l.Released = func(*sql.Tx, *model.Bench) error {
				t.Fatal("incomplete remote proof released stage history")
				return nil
			}
			if _, err := l.Release(context.Background(), owned.Key, owned.Lease.Token); err == nil {
				t.Fatal("incomplete/conflicting checkpoint permitted release")
			}
			if !reflect.DeepEqual(f.read(t), owned) {
				t.Fatal("remote mismatch changed retained ownership")
			}
		})
	}
}

func TestReleaseStoppedPartialPreparationAndHistoryAreAtomic(t *testing.T) {
	f, l, ready := availableLifecycle(t)
	owned, err := l.Acquire(context.Background(), ready.Key, "holder")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, exit := range []int{0, 17} {
		id, err := model.Nonce()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		raw := []byte("sealed stage evidence\n")
		log := "run-" + id + ".log"
		if err := os.WriteFile(filepath.Join(l.State, log), raw, 0400); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if err := l.Within(func(update Update) error {
			return update(func(rows benchstate.Database, tx *sql.Tx) error {
				if err := history.Start(tx, history.Record{ID: id, Key: owned.Key, Holder: "holder", StartedAt: time.Now().UTC()}); err != nil {
					return err
				}
				if err := history.Seal(tx, id, reap.Entry{Path: log, SHA256: hex.EncodeToString(sum[:])}, int64(len(raw)), &exit, false); err != nil {
					return err
				}
				rows[owned.Key].Lease.RetainedRuns = append(rows[owned.Key].Lease.RetainedRuns, id)
				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Within(func(update Update) error {
		return update(func(rows benchstate.Database, _ *sql.Tx) error {
			b := rows[owned.Key]
			b.Workspace.Preparation = []model.WorkspacePreparation{{Name: "a", Digest: strings.Repeat("a", 64), RunID: ids[0]}}
			b.Lease.StopRequested = true
			b.Lease.Continuation = ""
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	owned = f.read(t)
	l.Released = func(tx *sql.Tx, b *model.Bench) error {
		for _, id := range b.Lease.RetainedRuns {
			if err := history.Released(tx, id, b.Workspace.Checkpoint.Seal); err != nil {
				return err
			}
		}
		return nil
	}
	base := l.Within
	failed := false
	l.Within = func(fn func(Update) error) error {
		return base(func(update Update) error {
			return fn(func(change Change) error {
				return update(func(rows benchstate.Database, tx *sql.Tx) error {
					if err := change(rows, tx); err != nil {
						return err
					}
					if !failed && rows[owned.Key].Lease == nil {
						failed = true
						f.failSave = true
					}
					return nil
				})
			})
		})
	}
	result, err := l.Release(context.Background(), owned.Key, owned.Lease.Token)
	if err == nil || !failed || !reflect.DeepEqual(result, owned) || !reflect.DeepEqual(f.read(t), owned) {
		t.Fatal("failed release lost stopped partial preparation", err)
	}
	checkHistory := func(released bool) {
		t.Helper()
		if err := base(func(update Update) error {
			return update(func(_ benchstate.Database, tx *sql.Tx) error {
				for _, id := range ids {
					r, err := history.Get(tx, id)
					if err != nil {
						return err
					}
					if r.File == nil || r.ReleasedAt.IsZero() == released || released && r.Checkpoint != owned.Workspace.Checkpoint.Seal {
						t.Fatal("history release not atomic with ownership", released)
					}
				}
				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
	checkHistory(false)
	result, err = l.Release(context.Background(), owned.Key, owned.Lease.Token)
	if err != nil || result.Lease != nil || len(result.Workspace.Preparation) != 1 {
		t.Fatal("stopped partial preparation could not be released safely", err)
	}
	checkHistory(true)
}

func TestReleaseRefusesUnfencedWriterAndInitializationOwner(t *testing.T) {
	f, l, ready := availableLifecycle(t)
	owned, err := l.Acquire(context.Background(), ready.Key, "holder")
	if err != nil {
		t.Fatal(err)
	}
	l.Stopped = func(*model.Bench) error { return errors.New("scope still populated") }
	if result, err := l.Release(context.Background(), owned.Key, owned.Lease.Token); err == nil || result != nil {
		t.Fatal("unfenced writer was released", result, err)
	}
	if !reflect.DeepEqual(owned, f.read(t)) {
		t.Fatal("unfenced refusal changed owner")
	}
	f, l, initial := preparedLifecycle(t)
	if _, err := l.Release(context.Background(), initial.Key, initial.Lease.Token); err == nil {
		t.Fatal("ordinary release replaced initial preparation finalization")
	}
	if !reflect.DeepEqual(initial, f.read(t)) {
		t.Fatal("ordinary release changed initializing workspace")
	}
}

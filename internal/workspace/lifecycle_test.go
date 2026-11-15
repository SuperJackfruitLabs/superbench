package workspace

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func preparedLifecycle(t *testing.T) (*stagingFixture, Lifecycle, *model.Bench) {
	t.Helper()
	f := newStaging(t)
	b, err := f.manager.Stage(context.Background(), "create1", f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Within(func(update Update) error {
		return update(func(rows benchstate.Database, _ *sql.Tx) error {
			b := rows[f.spec.Key]
			b.Workspace.Phase = "prepared"
			for _, member := range b.Workspace.Members {
				b.Workspace.Preparation = append(b.Workspace.Preparation, model.WorkspacePreparation{Name: member.Name, Digest: strings.Repeat("a", 64)})
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	b = f.read(t)
	l := Lifecycle{State: f.manager.State, Data: f.manager.Data, Within: f.manager.Within, Git: f.manager.Git, Stopped: func(b *model.Bench) error {
		if !f.held || b.Lease == nil {
			t.Fatal("fencing authority called without locked ownership")
		}
		return nil
	}, Prepared: func(*model.Bench) error { return nil }, Released: func(_ *sql.Tx, b *model.Bench) error {
		if !f.held || b.Lease == nil || b.Workspace.Checkpoint == nil {
			t.Fatal("history release before complete owned checkpoint")
		}
		return nil
	}}
	return f, l, b
}

func remoteTask(t *testing.T, member model.WorkspaceMember) string {
	t.Helper()
	out := git(t, member.Mirror, "ls-remote", "--refs", "origin", "refs/heads/"+member.Branch)
	if out == "" {
		return ""
	}
	return strings.Fields(out)[0]
}

func rejectTask(t *testing.T, member model.WorkspaceMember) string {
	t.Helper()
	path := filepath.Join(member.Repository, ".git", "hooks", "pre-receive")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLifecycleCheckpointRejectsDirtySecondMemberBeforeAnyPush(t *testing.T) {
	f, l, b := preparedLifecycle(t)
	if err := os.WriteFile(filepath.Join(b.Workspace.Members[1].Path, "private-output"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := l.Checkpoint(context.Background(), b.Key, b.Lease.Token)
	if !errors.Is(err, model.ErrDirty) || got == nil {
		t.Fatal(got, err)
	}
	if persisted := f.read(t); !reflect.DeepEqual(persisted, b) {
		t.Fatal("dirty checkpoint changed authority")
	}
	for _, member := range b.Workspace.Members {
		if remoteTask(t, member) != "" {
			t.Fatal("partial push preceded whole-vector cleanliness")
		}
	}
}

func TestLifecycleCheckpointPartialPushRetainsFrozenGenerationAndRetries(t *testing.T) {
	f, l, b := preparedLifecycle(t)
	hook := rejectTask(t, b.Workspace.Members[1])
	got, err := l.Checkpoint(context.Background(), b.Key, b.Lease.Token)
	if err == nil || got == nil || got.Workspace.Pending == nil || got.Workspace.Checkpoint != nil || got.Lease == nil {
		t.Fatal(got, err)
	}
	pending := privateRecord(got)
	if !reflect.DeepEqual(got, f.read(t)) {
		t.Fatal("failure returned uncommitted generation")
	}
	if remoteTask(t, b.Workspace.Members[0]) != b.Workspace.Members[0].Source || remoteTask(t, b.Workspace.Members[1]) != "" {
		t.Fatal("fixture did not fail after first publication")
	}
	meta, _ := checkpointset.MetadataRef(b.Workspace.ID)
	if got := git(t, b.Workspace.Members[0].Mirror, "ls-remote", "--refs", "origin", meta); got != "" {
		t.Fatal("partial generation has a final seal")
	}
	// Other users can move remote source branches while this failed generation
	// remains held. Retrying must use its saved HEAD vector and generation.
	git(t, b.Workspace.Members[1].Repository, "commit", "--allow-empty", "-m", "new remote main")
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	complete, err := l.Checkpoint(context.Background(), b.Key, b.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if complete.Workspace.Pending != nil || complete.Workspace.Checkpoint == nil || complete.Lease == nil || complete.Workspace.Phase != "prepared" {
		t.Fatal("checkpoint published readiness or lost ownership")
	}
	cp := complete.Workspace.Checkpoint
	if cp.GenerationID != pending.Workspace.Pending.GenerationID || cp.Digest != pending.Workspace.Pending.Digest || !reflect.DeepEqual(cp.Members, pending.Workspace.Pending.Members) {
		t.Fatal("retry changed frozen generation")
	}
	for i, member := range b.Workspace.Members {
		if remoteTask(t, member) != cp.Members[i].Commit {
			t.Fatal("remote task differs from complete vector")
		}
	}
	if !reflect.DeepEqual(complete, f.read(t)) {
		t.Fatal("completed checkpoint not durable")
	}
	// Returned checkpoint slices and pointers cannot mutate persisted/pending data.
	complete.Workspace.Checkpoint.Members[0].Commit = "caller-mutated"
	if f.read(t).Workspace.Checkpoint.Members[0].Commit == "caller-mutated" {
		t.Fatal("checkpoint metadata escaped by alias")
	}
}

func TestLifecyclePendingCheckpointRefusesNewHEADWithoutReplacingGeneration(t *testing.T) {
	f, l, b := preparedLifecycle(t)
	rejectTask(t, b.Workspace.Members[1])
	pending, err := l.Checkpoint(context.Background(), b.Key, b.Lease.Token)
	if err == nil || pending.Workspace.Pending == nil {
		t.Fatal("missing partial generation", err)
	}
	member := b.Workspace.Members[1]
	git(t, member.Path, "commit", "--allow-empty", "-m", "new held work")
	want := git(t, member.Path, "rev-parse", "HEAD")
	got, err := l.Checkpoint(context.Background(), b.Key, b.Lease.Token)
	if err == nil || !reflect.DeepEqual(got, pending) || !reflect.DeepEqual(f.read(t), pending) {
		t.Fatal("pending generation replaced after local HEAD changed", err)
	}
	if git(t, member.Path, "rev-parse", "HEAD") != want {
		t.Fatal("new held work was reset")
	}
}

func TestLifecycleFinalSaveFailureRetainsPendingAndRecoversPublishedSeal(t *testing.T) {
	f, l, b := preparedLifecycle(t)
	base := l.Within
	failed := false
	l.Within = func(fn func(Update) error) error {
		return base(func(update Update) error {
			return fn(func(change Change) error {
				return update(func(rows benchstate.Database, tx *sql.Tx) error {
					if err := change(rows, tx); err != nil {
						return err
					}
					if !failed && rows[b.Key].Workspace.Checkpoint != nil && rows[b.Key].Workspace.Pending == nil {
						failed = true
						f.failSave = true
					}
					return nil
				})
			})
		})
	}
	pending, err := l.Checkpoint(context.Background(), b.Key, b.Lease.Token)
	if err == nil || !failed || pending.Workspace.Pending == nil || pending.Workspace.Checkpoint != nil {
		t.Fatal("completion save failure lost durable pending owner", err)
	}
	if !reflect.DeepEqual(pending, f.read(t)) {
		t.Fatal("returned final-save result was not committed")
	}
	meta, _ := checkpointset.MetadataRef(b.Workspace.ID)
	seal := strings.Fields(git(t, b.Workspace.Members[0].Mirror, "ls-remote", "--refs", "origin", meta))[0]
	complete, err := l.Checkpoint(context.Background(), b.Key, b.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if complete.Workspace.Checkpoint.GenerationID != pending.Workspace.Pending.GenerationID || complete.Workspace.Checkpoint.Seal != seal || complete.Lease.Token != b.Lease.Token {
		t.Fatal("lost reply retry changed generation, seal or ownership")
	}
}

func TestLifecycleFinishInitializationRequiresIndependentRemoteProof(t *testing.T) {
	f, l, b := preparedLifecycle(t)
	base := l.Within
	sessions, releases := 0, 0
	var changedPin, original string
	l.Within = func(fn func(Update) error) error {
		sessions++
		if sessions == 3 {
			cp := f.read(t).Workspace.Checkpoint
			if cp == nil {
				t.Fatal("independent verification preceded checkpoint")
			}
			changedPin, _ = checkpointset.PinRef(b.Workspace.ID, cp.GenerationID, b.Workspace.Members[1].Name)
			original = cp.Members[1].Commit
			remote := b.Workspace.Members[1].Repository
			git(t, remote, "commit", "--allow-empty", "-m", "remote administrator changes pin")
			git(t, remote, "update-ref", changedPin, git(t, remote, "rev-parse", "HEAD"))
		}
		return base(fn)
	}
	l.Released = func(_ *sql.Tx, _ *model.Bench) error { releases++; return nil }
	got, err := l.FinishInitialization(context.Background(), b.Key, b.Lease.Token)
	if err == nil || got == nil || got.Lease == nil || got.Workspace.Phase != "prepared" || releases != 0 {
		t.Fatal("readiness escaped failed independent pin proof", err)
	}
	if !reflect.DeepEqual(got, f.read(t)) {
		t.Fatal("failed readiness changed committed owner")
	}
	git(t, b.Workspace.Members[1].Repository, "update-ref", changedPin, original)
	ready, err := l.FinishInitialization(context.Background(), b.Key, b.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Lease != nil || ready.Workspace.Phase != "ready" || releases != 1 || !reflect.DeepEqual(ready, f.read(t)) {
		t.Fatal("complete initialization not atomically released")
	}
}

func TestLifecycleRefusesUnpreparedOrUnfencedInitialization(t *testing.T) {
	f := newStaging(t)
	b, err := f.manager.Stage(context.Background(), "create1", f.spec)
	if err != nil {
		t.Fatal(err)
	}
	l := Lifecycle{State: f.manager.State, Data: f.manager.Data, Within: f.manager.Within, Git: f.manager.Git, Stopped: func(*model.Bench) error { return nil }, Prepared: func(*model.Bench) error { return nil }, Released: func(*sql.Tx, *model.Bench) error { t.Fatal("unprepared release"); return nil }}
	if _, err := l.FinishInitialization(context.Background(), b.Key, b.Lease.Token); err == nil {
		t.Fatal("staging inferred readiness")
	}
	if !reflect.DeepEqual(f.read(t), b) {
		t.Fatal("unprepared initialization mutated")
	}
	f, l, b = preparedLifecycle(t)
	l.Stopped = func(*model.Bench) error { return errors.New("unfenced writer") }
	if _, err := l.Checkpoint(context.Background(), b.Key, b.Lease.Token); err == nil {
		t.Fatal("unfenced checkpoint accepted")
	}
	if !reflect.DeepEqual(f.read(t), b) {
		t.Fatal("unfenced attempt changed checkpoint authority")
	}
}

func TestLifecycleInspectRefusesChangedOriginAndUntrackedRootOutput(t *testing.T) {
	f, l, b := preparedLifecycle(t)
	member := b.Workspace.Members[1]
	git(t, member.Mirror, "config", "remote.origin.url", b.Workspace.Members[0].Repository)
	if _, err := l.Inspect(context.Background(), b, false); err == nil {
		t.Fatal("changed member origin accepted")
	}
	git(t, member.Mirror, "config", "remote.origin.url", member.Repository)
	if err := os.WriteFile(filepath.Join(b.Path, "root-output"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Checkpoint(context.Background(), b.Key, b.Lease.Token); !errors.Is(err, model.ErrDirty) {
		t.Fatal("untracked root output ignored", err)
	}
	if !reflect.DeepEqual(f.read(t), b) {
		t.Fatal("invalid checkout changed checkpoint state")
	}
}

func TestLifecycleReadinessSaveFailureRollsBackHistoryRelease(t *testing.T) {
	f, l, b := preparedLifecycle(t)
	base := l.Within
	failed := false
	l.Within = func(fn func(Update) error) error {
		return base(func(update Update) error {
			return fn(func(change Change) error {
				return update(func(rows benchstate.Database, tx *sql.Tx) error {
					if _, err := tx.Exec("CREATE TABLE IF NOT EXISTS lifecycle_release_proof(id TEXT PRIMARY KEY)"); err != nil {
						return err
					}
					if err := change(rows, tx); err != nil {
						return err
					}
					if !failed && rows[b.Key].Workspace.Phase == "ready" {
						failed = true
						f.failSave = true
					}
					return nil
				})
			})
		})
	}
	l.Released = func(tx *sql.Tx, _ *model.Bench) error {
		_, err := tx.Exec("INSERT INTO lifecycle_release_proof VALUES ('retained-run')")
		return err
	}
	held, err := l.FinishInitialization(context.Background(), b.Key, b.Lease.Token)
	if err == nil || !failed || held == nil || held.Lease == nil || held.Workspace.Phase != "prepared" || held.Workspace.Checkpoint == nil {
		t.Fatal("readiness persistence failure lost complete held state", err)
	}
	if !reflect.DeepEqual(held, f.read(t)) {
		t.Fatal("returned uncommitted readiness")
	}
	if err := base(func(update Update) error {
		return update(func(_ benchstate.Database, tx *sql.Tx) error {
			var count int
			if err := tx.QueryRow("SELECT count(*) FROM lifecycle_release_proof").Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatal("history release survived failed readiness transaction")
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	ready, err := l.FinishInitialization(context.Background(), b.Key, b.Lease.Token)
	if err != nil || ready.Lease != nil || ready.Workspace.Phase != "ready" {
		t.Fatal("readiness retry failed", err)
	}
}

func TestLifecycleFinishRefusesStoppedInitializationContinuation(t *testing.T) {
	f, l, b := preparedLifecycle(t)
	if err := f.manager.Within(func(update Update) error {
		return update(func(rows benchstate.Database, _ *sql.Tx) error {
			rows[b.Key].Lease.StopRequested = true
			rows[b.Key].Lease.Continuation = ""
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	stopped := f.read(t)
	if _, err := l.FinishInitialization(context.Background(), b.Key, b.Lease.Token); err == nil {
		t.Fatal("operator stop revived initialization")
	}
	if !reflect.DeepEqual(stopped, f.read(t)) {
		t.Fatal("stopped initialization changed")
	}
	for _, member := range b.Workspace.Members {
		if remoteTask(t, member) != "" {
			t.Fatal("stopped initialization published a task branch")
		}
	}
}

func TestLifecycleFinishRefusesTaskTipChangedAfterSeal(t *testing.T) {
	f, l, b := preparedLifecycle(t)
	base := l.Within
	sessions, releases := 0, 0
	l.Within = func(fn func(Update) error) error {
		sessions++
		if sessions == 3 {
			member := b.Workspace.Members[1]
			git(t, member.Repository, "commit", "--allow-empty", "-m", "remote task advances after seal")
			git(t, member.Repository, "update-ref", "refs/heads/"+member.Branch, git(t, member.Repository, "rev-parse", "HEAD"))
		}
		return base(fn)
	}
	l.Released = func(*sql.Tx, *model.Bench) error { releases++; return nil }
	got, err := l.FinishInitialization(context.Background(), b.Key, b.Lease.Token)
	if err == nil || got == nil || got.Lease == nil || got.Workspace.Phase != "prepared" || releases != 0 {
		t.Fatal("historical seal alone authorized release", err)
	}
	if !reflect.DeepEqual(got, f.read(t)) {
		t.Fatal("task-tip mismatch changed held ownership")
	}
}

func TestLifecycleFinishRevalidatesPreparationBeforeRelease(t *testing.T) {
	f, l, b := preparedLifecycle(t)
	base := l.Within
	sessions, releases := 0, 0
	stale := false
	l.Within = func(fn func(Update) error) error {
		sessions++
		if sessions == 3 {
			stale = true
		}
		return base(fn)
	}
	l.Prepared = func(*model.Bench) error {
		if stale {
			return errors.New("current member configuration differs from preparation fingerprint")
		}
		return nil
	}
	l.Released = func(*sql.Tx, *model.Bench) error { releases++; return nil }
	got, err := l.FinishInitialization(context.Background(), b.Key, b.Lease.Token)
	if err == nil || got == nil || got.Workspace.Checkpoint == nil || got.Workspace.Phase != "prepared" || got.Lease == nil || releases != 0 {
		t.Fatal("stale preparation authorized readiness", err)
	}
	if !reflect.DeepEqual(got, f.read(t)) {
		t.Fatal("stale preparation changed held checkpoint")
	}
}

func TestLifecycleInspectMemberRechecksPhysicalTargetWithoutOtherMemberGit(t *testing.T) {
	_, l, b := preparedLifecycle(t)
	member := b.Workspace.Members[1]
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	// Simulate an earlier member's hook replacing a later member after the
	// initial whole-vector inspection. Keep the original checkout preserved.
	if _, err := l.Inspect(context.Background(), b, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(member.Path, member.Path+"-preserved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, member.Path); err != nil {
		t.Fatal(err)
	}
	realGit := l.Git
	l.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		if dir == member.Path || dir == member.Mirror {
			t.Fatal("physical-target refusal must precede selected member Git/cache effects")
		}
		return realGit(ctx, dir, args...)
	}
	if _, err := l.InspectMember(context.Background(), b, member.Name, false); err == nil {
		t.Fatal("symlink replacement granted member authority")
	}
	// A targeted check reads no other member's Git metadata. Preparation calls
	// it for each member immediately before that member's effects.
	first, err := l.InspectMember(context.Background(), b, "a", false)
	if err != nil || first.Name != "a" || first.Commit != b.Workspace.Members[0].Source {
		t.Fatal("selected-member inspection depended on another member", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatal("refused target acquired cache/output effects", err)
	}
}

func TestLifecycleInspectMemberRefusesForeignCommonDirectory(t *testing.T) {
	_, l, b := preparedLifecycle(t)
	member := b.Workspace.Members[1]
	outside := filepath.Join(t.TempDir(), "foreign")
	git(t, filepath.Dir(outside), "clone", "--", member.Repository, outside)
	git(t, outside, "checkout", "-b", member.Branch)
	if err := os.Rename(member.Path, member.Path+"-preserved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(outside, member.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := l.InspectMember(context.Background(), b, member.Name, false); err == nil {
		t.Fatal("same-origin foreign checkout bypassed registered mirror identity")
	}
}

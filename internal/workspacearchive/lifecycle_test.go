package workspacearchive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

type fixture struct {
	manager        Manager
	ready          *model.Bench
	held, failSave bool
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	command := gitcontrol.Command(ctx, dir, args...)
	command.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	raw, err := command.CombinedOutput()
	if err != nil {
		return string(raw), errors.New(string(raw) + ": " + err.Error())
	}
	return string(raw), nil
}
func gitOK(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(args, err)
	}
	return strings.TrimSpace(out)
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	state, data := filepath.Join(root, "state"), filepath.Join(root, "data")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	f := &fixture{}
	f.manager = Manager{State: state, Data: data, Git: git, Busy: errors.New("busy"), ReleaseQuota: func(b *model.Bench) error { return os.Remove(b.Path) }}
	f.manager.Within = func(fn func(workspace.Update) error) error {
		return statestore.Within(state, nil, func(session *statestore.Session) error {
			f.held = true
			defer func() { f.held = false }()
			return fn(func(change workspace.Change) error {
				return session.Update(func(tx *sql.Tx) error {
					if err := benchstate.Migrate(tx, state, func(*model.Bench) error { return nil }); err != nil {
						return err
					}
					records, err := benchstate.Load(tx)
					if err != nil {
						return err
					}
					before := benchstate.Capture(records)
					if err := change(records, tx); err != nil {
						return err
					}
					if f.failSave {
						f.failSave = false
						return errors.New("injected SQL failure")
					}
					if err := benchstate.Validate(records); err != nil {
						return err
					}
					if err := benchstate.ValidateTransition(before, records); err != nil {
						return err
					}
					for key, b := range records {
						raw, err := json.Marshal(b)
						if err != nil {
							return err
						}
						if _, err := tx.Exec("INSERT INTO benches VALUES (?,?) ON CONFLICT(key) DO UPDATE SET record=excluded.record", key, string(raw)); err != nil {
							return err
						}
					}
					return nil
				})
			})
		})
	}
	f.manager.SaveIntent = func(path string, raw []byte) error {
		if !f.held {
			t.Fatal("intent outside host lock")
		}
		return os.WriteFile(path, raw, 0600)
	}
	spec := workspace.Spec{Key: "card", CheckpointRepository: "a"}
	for _, name := range []string{"a", "b"} {
		remote := filepath.Join(root, name)
		if err := os.Mkdir(remote, 0700); err != nil {
			t.Fatal(err)
		}
		gitOK(t, remote, "init", "-b", "main")
		if err := os.WriteFile(filepath.Join(remote, "README"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remote, ".gitignore"), []byte("cache.out\n"), 0600); err != nil {
			t.Fatal(err)
		}
		gitOK(t, remote, "add", ".")
		gitOK(t, remote, "commit", "-m", "initial")
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: name, Repository: remote})
	}
	staging := workspace.Manager{State: state, Data: data, Within: f.manager.Within, Git: git, Admit: func() error { return nil }, Allocate: func(b *model.Bench) error {
		b.ProjectID = 10001
		b.QuotaBytes = 32 << 20
		b.QuotaInodes = 1000
		return nil
	}}
	staged, err := staging.Stage(context.Background(), "create1", spec)
	if err != nil {
		t.Fatal(err)
	}
	f.change(t, func(b *model.Bench) {
		b.Workspace.Phase = "prepared"
		for _, member := range b.Workspace.Members {
			b.Workspace.Preparation = append(b.Workspace.Preparation, model.WorkspacePreparation{Name: member.Name, Digest: strings.Repeat("a", 64)})
		}
	})
	lifecycle := workspace.Lifecycle{State: state, Data: data, Git: git, Within: f.manager.Within, Stopped: func(*model.Bench) error {
		if !f.held {
			t.Fatal("fence outside host lock")
		}
		return nil
	}, Prepared: func(*model.Bench) error { return nil }, Released: func(*sql.Tx, *model.Bench) error { return nil }}
	f.ready, err = lifecycle.FinishInitialization(context.Background(), "card", staged.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(data, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	return f
}
func (f *fixture) read(t *testing.T) *model.Bench {
	t.Helper()
	db, err := statestore.Open(filepath.Join(f.manager.State, "bench.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	records, err := benchstate.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	return records["card"]
}
func (f *fixture) change(t *testing.T, change func(*model.Bench)) {
	t.Helper()
	if err := f.manager.Within(func(update workspace.Update) error {
		return update(func(records benchstate.Database, _ *sql.Tx) error { change(records["card"]); return nil })
	}); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) archive(t *testing.T) *model.Bench {
	t.Helper()
	b, err := f.manager.Archive(context.Background(), "card", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestArchiveWholeTreeAndReviewedReapRetryAcrossCleanupAndQuotaRetirement(t *testing.T) {
	f := newFixture(t)
	output := filepath.Join(f.ready.Workspace.Members[1].Path, "cache.out")
	if err := os.WriteFile(output, []byte("ignored retained bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	archived, err := f.manager.Archive(context.Background(), "card", 0, func(_ context.Context, b *model.Bench) error {
		calls++
		if !f.held || b.Workspace.Archive == nil {
			t.Fatal("trigger lacks locked archive candidate")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || archived.Lease != nil || archived.Workspace.Phase != "archived" || archived.Workspace.Archive.FrozenAt == "" {
		t.Fatal("archive omitted whole-tree read-only completion")
	}
	for _, path := range []string{archived.Path, output, filepath.Join(archived.Workspace.Members[0].Path, ".git")} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm()&0222 != 0 {
			t.Fatal("archive entry writable", path, err)
		}
	}
	if _, err := f.manager.Archive(context.Background(), "card", time.Hour, func(context.Context, *model.Bench) error { t.Fatal("retry repeated trigger"); return nil }); err != nil {
		t.Fatal(err)
	}
	plan, err := f.manager.PlanReap(context.Background(), "card")
	if err != nil {
		t.Fatal(err)
	}
	recoverable := map[string]bool{}
	for _, e := range plan.Tree.Entries {
		recoverable[e.Path] = e.Recoverable
	}
	if !recoverable["a/README"] || recoverable["a/.git"] || recoverable["b/cache.out"] {
		t.Fatal("plan misrepresented recoverable and retained bytes", recoverable)
	}
	raw, err := plan.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadPlan(raw)
	if err != nil || !reflect.DeepEqual(plan, decoded) {
		t.Fatal("reviewed plan roundtrip", err)
	}
	f.manager.AfterDelete = func() error { return errors.New("interrupt after content deletion") }
	partial, err := f.manager.Reap(context.Background(), *plan)
	if err == nil || partial.Workspace.Phase != "reaping" || !reflect.DeepEqual(partial, f.read(t)) {
		t.Fatal("deletion failure lost durable approval", err)
	}
	for _, member := range partial.Workspace.Members {
		if _, err := os.Lstat(member.Path); !os.IsNotExist(err) {
			t.Fatal("fixture did not delete member tree", err)
		}
	}
	f.manager.AfterDelete = nil
	realGit := f.manager.Git
	failed := false
	f.manager.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "worktree" && args[1] == "remove" && dir == partial.Workspace.Members[1].Mirror && !failed {
			failed = true
			return "", errors.New("second registration interrupted")
		}
		return realGit(ctx, dir, args...)
	}
	if _, err := f.manager.Reap(context.Background(), *plan); err == nil || !failed {
		t.Fatal("registration failure not retained")
	}
	f.manager.Git = realGit
	retirements := 0
	f.manager.ReleaseQuota = func(b *model.Bench) error {
		if !f.held || b.ProjectID != archived.ProjectID {
			t.Fatal("quota retirement lost locked identity")
		}
		retirements++
		if err := os.Remove(b.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
		if retirements == 1 {
			f.failSave = true
		}
		return nil
	}
	partial, err = f.manager.Reap(context.Background(), *plan)
	if err == nil || partial.Workspace.Phase != "reaping" || partial.Workspace.Archive.ReapedAt != "" {
		t.Fatal("failed final save falsely reported reaped", err)
	}
	reaped, err := f.manager.Reap(context.Background(), *plan)
	if err != nil {
		t.Fatal(err)
	}
	if retirements != 2 || reaped.Workspace.Phase != "reaped" || reaped.Workspace.Archive.ReapedAt == "" || !reflect.DeepEqual(reaped.Workspace.Members, archived.Workspace.Members) || !reflect.DeepEqual(reaped.Workspace.Checkpoint, archived.Workspace.Checkpoint) || reaped.ProjectID != archived.ProjectID {
		t.Fatal("retry erased identity/evidence or skipped quota retirement")
	}
	for _, member := range reaped.Workspace.Members {
		registrations := gitOK(t, member.Mirror, "worktree", "list", "--porcelain")
		if strings.Contains(registrations, member.Path) {
			t.Fatal("member registration survived reap")
		}
		if gitOK(t, member.Mirror, "rev-parse", "refs/heads/"+member.Branch) != member.Source {
			t.Fatal("reap deleted task refs")
		}
	}
	replay, err := f.manager.Reap(context.Background(), *plan)
	if err != nil || !reflect.DeepEqual(replay, reaped) || retirements != 2 {
		t.Fatal("completed reap replay changed state", err)
	}
}

func TestArchiveDirtyLaterMemberRefusesBeforeAnyPermissionIntent(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.ready.Workspace.Members[1].Path, "untracked"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	f.manager.SaveIntent = func(string, []byte) error { t.Fatal("dirty vector reached intent publication"); return nil }
	if _, err := f.manager.Archive(context.Background(), "card", 0, nil); !errors.Is(err, model.ErrDirty) {
		t.Fatal("dirty archive accepted", err)
	}
	if !reflect.DeepEqual(f.ready, f.read(t)) {
		t.Fatal("dirty archive changed state")
	}
	for _, member := range f.ready.Workspace.Members {
		info, err := os.Stat(member.Path)
		if err != nil || info.Mode().Perm()&0200 == 0 {
			t.Fatal("dirty archive partially froze a member", err)
		}
	}
}

func TestArchiveFailureAfterFreezeRetainsExactIntentAndRetryDoesNotRepeatTrigger(t *testing.T) {
	f := newFixture(t)
	within := f.manager.Within
	f.manager.Within = func(fn func(workspace.Update) error) error {
		return within(func(update workspace.Update) error {
			count := 0
			return fn(func(change workspace.Change) error {
				count++
				if count == 2 {
					f.failSave = true
				}
				return update(change)
			})
		})
	}
	checks := 0
	partial, err := f.manager.Archive(context.Background(), "card", 0, func(context.Context, *model.Bench) error { checks++; return nil })
	if err == nil || partial == nil || partial.Workspace.Phase != "archived" || partial.Workspace.Archive.FrozenAt != "" || !reflect.DeepEqual(partial, f.read(t)) {
		t.Fatal("failed save claimed read-only completion", err)
	}
	digest := partial.Workspace.Archive.FreezeDigest
	info, err := os.Stat(partial.Path)
	if err != nil || info.Mode().Perm()&0222 != 0 {
		t.Fatal("fixture did not complete permission effects", err)
	}
	f.manager.Within = within
	ready, err := f.manager.Archive(context.Background(), "card", time.Hour, func(context.Context, *model.Bench) error { t.Fatal("retry repeated completion trigger"); return nil })
	if err != nil || checks != 1 || ready.Workspace.Archive.FrozenAt == "" || ready.Workspace.Archive.FreezeDigest != digest || ready.Workspace.Archive.ReapAfter != partial.Workspace.Archive.ReapAfter {
		t.Fatal("archive retry replaced original authority", err)
	}
}

func TestArchiveFirstSaveFailureReturnsOriginalRecordAndDoesNotFreeze(t *testing.T) {
	f := newFixture(t)
	save := f.manager.SaveIntent
	f.manager.SaveIntent = func(path string, raw []byte) error {
		if err := save(path, raw); err != nil {
			return err
		}
		f.failSave = true
		return nil
	}
	result, err := f.manager.Archive(context.Background(), "card", 0, nil)
	if err == nil || !reflect.DeepEqual(result, f.ready) || !reflect.DeepEqual(f.read(t), f.ready) {
		t.Fatal("failed intent save returned uncommitted archive", err)
	}
	info, err := os.Stat(f.ready.Path)
	if err != nil || info.Mode().Perm()&0200 == 0 {
		t.Fatal("permission effects preceded durable intent", err)
	}
	f.manager.SaveIntent = save
	f.archive(t)
}

func TestReapRefusesChangedReviewedBytesAndUnelapsedGrace(t *testing.T) {
	for _, kind := range []string{"changed", "grace"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			grace := time.Duration(0)
			if kind == "grace" {
				grace = time.Hour
			}
			archived, err := f.manager.Archive(context.Background(), "card", grace, nil)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := f.manager.PlanReap(context.Background(), "card")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "changed" {
				path := filepath.Join(archived.Workspace.Members[1].Path, "README")
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("changed after review"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0400); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.manager.Reap(context.Background(), *plan); err == nil {
				t.Fatal("unsafe reap accepted")
			}
			if !reflect.DeepEqual(archived, f.read(t)) {
				t.Fatal("refused reap committed deletion authority")
			}
			if _, err := os.Stat(filepath.Join(archived.Workspace.Members[0].Path, "README")); err != nil {
				t.Fatal("refused reap partially deleted first member", err)
			}
		})
	}
}

func TestReapRetryRejectsChangedAndLinkedJournalsBeforeRegistrationCleanup(t *testing.T) {
	for _, kind := range []string{"unknown", "duplicate", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.archive(t)
			plan, err := f.manager.PlanReap(context.Background(), "card")
			if err != nil {
				t.Fatal(err)
			}
			f.manager.AfterDelete = func() error { return errors.New("pause before cleanup") }
			partial, err := f.manager.Reap(context.Background(), *plan)
			if err == nil {
				t.Fatal("fixture did not pause")
			}
			path := f.manager.reapJournal("card")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unknown":
				raw = append([]byte(`{"Unexpected":true,`), raw[1:]...)
			case "duplicate":
				raw = append([]byte(`{"Version":"ignored",`), raw[1:]...)
			case "symlink":
				if err := os.Rename(path, path+"-preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-preserved", path); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "symlink" {
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			f.manager.AfterDelete = nil
			if _, err := f.manager.Reap(context.Background(), *plan); err == nil {
				t.Fatal("changed retained journal accepted")
			}
			if !reflect.DeepEqual(partial, f.read(t)) {
				t.Fatal("journal rejection changed retained intent")
			}
			for _, member := range partial.Workspace.Members {
				if !strings.Contains(gitOK(t, member.Mirror, "worktree", "list", "--porcelain"), member.Path) {
					t.Fatal("journal rejection removed registration")
				}
			}
		})
	}
}

func TestArchiveTriggerCanOnlyAddMergeEvidence(t *testing.T) {
	f := newFixture(t)
	cases := map[string]func(*model.Bench){
		"nil-workspace": func(b *model.Bench) { b.Workspace = nil },
		"nil-archive":   func(b *model.Bench) { b.Workspace.Archive = nil },
		"freeze-digest": func(b *model.Bench) { b.Workspace.Archive.FreezeDigest = strings.Repeat("f", 64) },
		"timestamp":     func(b *model.Bench) { b.Workspace.Archive.ArchivedAt = "2030-01-01T00:00:00Z" },
		"deadline":      func(b *model.Bench) { b.Workspace.Archive.ReapAfter = "2030-01-01T00:00:00Z" },
		"phase":         func(b *model.Bench) { b.Workspace.Phase = "reaped" },
		"identity":      func(b *model.Bench) { b.Workspace.ID = strings.Repeat("f", 32) },
	}
	f.manager.SaveIntent = func(string, []byte) error { t.Fatal("invalid trigger reached intent publication"); return nil }
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			b, err := f.manager.Archive(context.Background(), "card", 0, func(_ context.Context, b *model.Bench) error { change(b); return nil })
			if err == nil || !reflect.DeepEqual(b, f.ready) || !reflect.DeepEqual(f.read(t), f.ready) {
				t.Fatal("malicious trigger escaped private candidate", err)
			}
			info, err := os.Stat(f.ready.Path)
			if err != nil || info.Mode().Perm()&0200 == 0 {
				t.Fatal("invalid trigger froze checkout", err)
			}
		})
	}
}

package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

type stagingFixture struct {
	manager     Manager
	spec        Spec
	held        bool
	commits     int
	failSave    bool
	allocations int
	admissions  int
}

func newStaging(t *testing.T) *stagingFixture {
	t.Helper()
	root := t.TempDir()
	a, b := remote(t, root, "a", ""), remote(t, root, "b", "")
	p := planner(t, root)
	f := &stagingFixture{spec: Spec{Key: "card", CheckpointRepository: "a", Repositories: []Repository{{Name: "a", Repository: a}, {Name: "b", Repository: b}}}}
	f.manager = Manager{State: p.Mirrors.State, Data: p.Mirrors.Data, Git: p.Git, Admit: func() error {
		f.admissions++
		if !f.held || f.commits == 0 {
			t.Fatal("admission preceded locked lookup")
		}
		return nil
	}}
	f.manager.Within = func(fn func(Update) error) error {
		return statestore.Within(f.manager.State, nil, func(s *statestore.Session) error {
			f.held = true
			defer func() { f.held = false }()
			return fn(func(change Change) error {
				err := s.Update(func(tx *sql.Tx) error {
					if err := benchstate.Migrate(tx, f.manager.State, func(*model.Bench) error { return nil }); err != nil {
						return err
					}
					rows, err := benchstate.Load(tx)
					if err != nil {
						return err
					}
					before := benchstate.Capture(rows)
					if err = change(rows, tx); err != nil {
						return err
					}
					if f.failSave {
						f.failSave = false
						return errors.New("injected durable save failure")
					}
					if err = benchstate.Validate(rows); err != nil {
						return err
					}
					if err = benchstate.ValidateTransition(before, rows); err != nil {
						return err
					}
					for k, b := range rows {
						raw, err := json.Marshal(b)
						if err != nil {
							return err
						}
						if _, err = tx.Exec("INSERT INTO benches VALUES (?,?) ON CONFLICT(key) DO UPDATE SET record=excluded.record", k, string(raw)); err != nil {
							return err
						}
					}
					return nil
				})
				if err == nil {
					f.commits++
				}
				return err
			})
		})
	}
	f.manager.Allocate = func(b *model.Bench) error {
		f.allocations++
		stored := f.read(t)
		if !f.held || stored == nil || stored.Workspace == nil || stored.Lease == nil || len(stored.Workspace.Members) != 2 {
			t.Fatal("allocation preceded durable vector/owner")
		}
		b.ProjectID = 10001
		b.QuotaBytes = 1 << 30
		b.QuotaInodes = 1000
		return nil
	}
	return f
}
func (f *stagingFixture) read(t *testing.T) *model.Bench {
	t.Helper()
	db, err := statestore.Open(filepath.Join(f.manager.State, "bench.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := benchstate.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	return rows[f.spec.Key]
}

func TestStagePreservesPartialCheckoutAndFrozenSourceAcrossRetry(t *testing.T) {
	f := newStaging(t)
	realGit := f.manager.Git
	failed := false
	f.manager.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "worktree" && args[1] == "add" {
			stored := f.read(t)
			if !f.held || stored.ProjectID == 0 {
				t.Fatal("checkout before durable allocation")
			}
			if dir == stored.Workspace.Members[1].Mirror && !failed {
				failed = true
				return "", errors.New("injected second checkout failure")
			}
		}
		return realGit(ctx, dir, args...)
	}
	b, err := f.manager.Stage(context.Background(), "create1", f.spec)
	if err == nil || b == nil || !failed || b.Workspace.Phase != "materializing" {
		t.Fatal(b, err)
	}
	first := b.Workspace.Members[0]
	second := b.Workspace.Members[1]
	if got := git(t, first.Path, "rev-parse", "HEAD"); got != first.Source {
		t.Fatal("first member lost")
	}
	if _, err := os.Stat(second.Path); !os.IsNotExist(err) {
		t.Fatal("second checkout exists", err)
	}
	persisted := f.read(t)
	if !reflect.DeepEqual(persisted, b) {
		t.Fatal("returned uncommitted identity")
	}
	git(t, second.Repository, "commit", "--allow-empty", "-m", "new source")
	b.Lease.Token = "caller-mutated"
	b.Workspace.Members[0].Source = "caller-mutated"
	f.manager.Git = realGit
	recovered, err := f.manager.Stage(context.Background(), "create1", f.spec)
	if err != nil || recovered.Workspace.Phase != "initializing" || recovered.Lease.Token != persisted.Lease.Token || recovered.Workspace.Members[1].Source != second.Source {
		t.Fatal(recovered, err)
	}
	for _, v := range recovered.Workspace.Members {
		if got := git(t, v.Path, "rev-parse", "HEAD"); got != v.Source {
			t.Fatal("retry reselected source", got)
		}
	}
	count := f.allocations
	f.manager.Git = func(context.Context, string, ...string) (string, error) {
		t.Fatal("initializing retry executed Git")
		return "", nil
	}
	if _, err := f.manager.Stage(context.Background(), "create1", f.spec); err != nil || f.allocations != count {
		t.Fatal("idempotent retry repeated effects", err)
	}
}

func TestStageAllocationSaveFailureKeepsDurableOwnerBeforeCheckout(t *testing.T) {
	f := newStaging(t)
	allocate := f.manager.Allocate
	f.manager.Allocate = func(b *model.Bench) error {
		if err := allocate(b); err != nil {
			return err
		}
		f.failSave = true
		return nil
	}
	b, err := f.manager.Stage(context.Background(), "create1", f.spec)
	if err == nil || b == nil || b.ProjectID != 0 || b.Workspace.Phase != "materializing" {
		t.Fatal(b, err)
	}
	if !reflect.DeepEqual(b, f.read(t)) {
		t.Fatal("allocation failure returned uncommitted state")
	}
	if _, err := os.Lstat(b.Path); !os.IsNotExist(err) {
		t.Fatal("checkout before allocation commit", err)
	}
	f.manager.Allocate = allocate
	done, err := f.manager.Stage(context.Background(), "create1", f.spec)
	if err != nil || done.Workspace.Phase != "initializing" || done.Lease.Token != b.Lease.Token {
		t.Fatal(done, err)
	}
}

func TestStageAllocatorCannotChangeIdentity(t *testing.T) {
	f := newStaging(t)
	allocate := f.manager.Allocate
	f.manager.Allocate = func(b *model.Bench) error {
		if err := allocate(b); err != nil {
			return err
		}
		b.Workspace.Members[0].Branch = "other"
		return nil
	}
	b, err := f.manager.Stage(context.Background(), "create1", f.spec)
	if err == nil || b == nil || b.ProjectID != 0 || b.Workspace.Members[0].Branch != "bench/card" {
		t.Fatal(b, err)
	}
	if !reflect.DeepEqual(b, f.read(t)) {
		t.Fatal("allocator changed durable metadata")
	}
	if _, err := os.Lstat(b.Path); !os.IsNotExist(err) {
		t.Fatal("allocator failure created checkout", err)
	}
}

func TestStageRefusesConflictsAndUnregisteredWorkWithoutEffects(t *testing.T) {
	for _, kind := range []string{"journal", "root", "scalar", "other-request", "other-spec", "explicit-quota"} {
		t.Run(kind, func(t *testing.T) {
			f := newStaging(t)
			if kind == "journal" {
				if err := os.WriteFile(filepath.Join(f.manager.State, "create-card.json"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "root" {
				if err := os.MkdirAll(filepath.Join(f.manager.Data, "benches", "card"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "scalar" {
				if err := f.manager.Within(func(update Update) error {
					return update(func(rows benchstate.Database, _ *sql.Tx) error {
						rows["card"] = &model.Bench{Key: "card", Repo: "/srv/repo", Path: "/srv/path", Mirror: "/srv/mirror"}
						return nil
					})
				}); err != nil {
					t.Fatal(err)
				}
			}
			request := "create1"
			spec := f.spec
			if kind == "other-request" || kind == "other-spec" || kind == "explicit-quota" {
				if _, err := f.manager.Stage(context.Background(), request, spec); err != nil {
					t.Fatal(err)
				}
				if kind == "other-request" {
					request = "create2"
				}
				if kind == "other-spec" {
					spec.Repositories = append([]Repository(nil), spec.Repositories...)
					spec.Repositories[1].Base = "other"
				}
				if kind == "explicit-quota" {
					spec.QuotaBytes = 1 << 30
				}
			}
			f.manager.Git = func(context.Context, string, ...string) (string, error) {
				t.Fatal("conflict performed Git")
				return "", nil
			}
			count := f.allocations
			admissions := f.admissions
			if _, err := f.manager.Stage(context.Background(), request, spec); err == nil {
				t.Fatal("conflict accepted")
			}
			if f.allocations != count || f.admissions != admissions {
				t.Fatal("conflict allocated or admitted")
			}
			if kind == "journal" {
				if raw, err := os.ReadFile(filepath.Join(f.manager.State, "create-card.json")); err != nil || string(raw) != "preserve" {
					t.Fatal("journal modified")
				}
			}
		})
	}
}

func TestStageRejectsInvalidOrCanceledRequestsBeforeSession(t *testing.T) {
	f := newStaging(t)
	f.manager.Within = func(func(Update) error) error { t.Fatal("invalid request entered session"); return nil }
	if err := ValidateStage("bad/id", f.spec); err == nil {
		t.Fatal("invalid creation ID accepted")
	}
	if _, err := f.manager.Stage(context.Background(), "bad/id", f.spec); err == nil {
		t.Fatal("invalid request accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.manager.Stage(ctx, "create1", f.spec); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	f.manager.Timeout = time.Hour + 1
	if _, err := f.manager.Stage(context.Background(), "create1", f.spec); err == nil {
		t.Fatal("invalid timeout accepted")
	}
}

func TestStagePureNamespaceConflictRefusesBeforeAdmission(t *testing.T) {
	f := newStaging(t)
	spec := f.spec
	spec.Repositories = append([]Repository(nil), spec.Repositories...)
	spec.Repositories[1].Repository = spec.Repositories[0].Repository
	spec.Repositories[1].Branch = "bench/card/child"
	f.manager.Within = func(func(Update) error) error { t.Fatal("pure conflict entered storage"); return nil }
	if _, err := f.manager.Stage(context.Background(), "create1", spec); err == nil {
		t.Fatal("namespace conflict accepted")
	}
}

func TestStageSourceSelectionUsesWholeOperationDeadline(t *testing.T) {
	f := newStaging(t)
	f.manager.Timeout = 100 * time.Millisecond
	f.manager.Git = func(ctx context.Context, _ string, _ ...string) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("source selection was unbounded")
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	b, err := f.manager.Stage(context.Background(), "create1", f.spec)
	if !errors.Is(err, context.DeadlineExceeded) || b != nil {
		t.Fatal(b, err)
	}
	if f.allocations != 0 || f.read(t) != nil {
		t.Fatal("timed-out selection published a plan")
	}
	if _, err := os.Lstat(filepath.Join(f.manager.Data, "benches", "card")); !os.IsNotExist(err) {
		t.Fatal("selection timeout created checkout", err)
	}
}

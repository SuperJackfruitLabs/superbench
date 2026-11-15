package workspacearchive_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/runprotection"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacearchive"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := gitcontrol.Command(context.Background(), dir, args...)
	c.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git%v:%v:%s", args, e, out)
	}
	return strings.TrimSpace(string(out))
}

type missingFixture struct {
	s       bench.Store
	m       workspacearchive.Manager
	b       *model.Bench
	p       *workspacetransfer.MissingPlan
	history []byte
}

func makeMissing(t *testing.T, whole bool) *missingFixture {
	t.Helper()
	root, e := filepath.EvalSymlinks(t.TempDir())
	must(t, e)
	f := &missingFixture{s: bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}}
	spec := workspace.Spec{Key: "source", CheckpointRepository: "a", QuotaBytes: 1 << 20}
	for _, name := range []string{"a", "b"} {
		r := filepath.Join(root, name)
		must(t, os.Mkdir(r, 0700))
		git(t, r, "init", "-b", "main")
		must(t, os.WriteFile(filepath.Join(r, "tracked"), []byte(name), 0600))
		must(t, os.WriteFile(filepath.Join(r, ".gitignore"), []byte("ignored\n"), 0600))
		git(t, r, "add", ".")
		git(t, r, "commit", "-m", "fixture")
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: name, Repository: r})
	}
	f.b, e = f.s.InitializeWorkspaceContext(context.Background(), "create-source", spec)
	must(t, e)

	owner, e := f.s.AcquireWorkspaceOwnerContext(context.Background(), "source", "lost-writer")
	must(t, e)
	_, e = f.s.StopWorkspace("source", owner.Lease.Token, false)
	must(t, e)
	admin, e := f.s.WorkspaceRepair()
	must(t, e)
	runID := strings.Repeat("b", 32)
	must(t, admin.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		if e := history.Start(tx, history.Record{ID: runID, Key: "source", Holder: "lost-writer", StartedAt: time.Now().UTC()}); e != nil {
			return e
		}
		r, e := history.Get(tx, runID)
		if e != nil {
			return e
		}
		digest, e := history.Digest(r)
		if e != nil {
			return e
		}
		r, e = history.ObserveInterrupted(tx, runID, digest, history.InterruptedObservation{ApprovalDigest: strings.Repeat("c", 64), PriorDigest: digest, LogState: "absent", ObservedAt: time.Now().UTC()}, nil, 0)
		if e != nil {
			return e
		}
		rows["source"].Lease.RetainedRuns = append(rows["source"].Lease.RetainedRuns, runID)
		f.history, e = json.Marshal(r)
		return e
	}))
	if whole {
		must(t, os.RemoveAll(f.b.Path))
	} else {
		must(t, os.RemoveAll(f.b.Workspace.Members[1].Path))
	}
	transfer := workspacetransfer.Manager{Store: f.s}
	f.p, e = transfer.InspectMissing(context.Background(), "source", "target", "missing1", 2<<20)
	must(t, e)
	_, e = transfer.BeginMissing(context.Background(), *f.p)
	must(t, e)
	_, e = transfer.ContinueMissing(context.Background(), "source", f.p.ID)
	must(t, e)
	target, e := f.s.AcquireWorkspaceOwnerContext(context.Background(), "target", "next-stage")
	must(t, e)
	tm := target.Workspace.Members[0]
	must(t, os.WriteFile(filepath.Join(tm.Path, "tracked"), []byte("destination advanced"), 0600))
	git(t, tm.Path, "add", "tracked")
	git(t, tm.Path, "commit", "-m", "next destination stage")
	_, e = f.s.CheckpointWorkspaceContext(context.Background(), "target", target.Lease.Token)
	must(t, e)
	_, e = f.s.ReleaseWorkspaceContext(context.Background(), "target", target.Lease.Token)
	must(t, e)

	admin, e = f.s.WorkspaceRepair()
	must(t, e)
	f.m = workspacearchive.Manager{State: f.s.State, Data: f.s.Data, Git: admin.Prep.Git, Within: admin.Lifecycle.Within, SaveIntent: func(path string, raw []byte) error { return os.WriteFile(path, raw, 0600) }}
	t.Cleanup(func() {
		_ = filepath.WalkDir(f.s.Data, func(path string, d os.DirEntry, e error) error {
			if e == nil && d.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	return f
}
func TestMissingTransferredArchiveReapPreservesDirtyBytesAndQuarantine(t *testing.T) {
	for _, whole := range []bool{false, true} {
		name := "mixed"
		if whole {
			name = "whole"
		}
		t.Run(name, func(t *testing.T) {
			f := makeMissing(t, whole)
			var staged string
			if !whole {
				a := f.b.Workspace.Members[0]
				must(t, os.WriteFile(filepath.Join(a.Path, "tracked"), []byte("staged unknown work"), 0600))
				git(t, a.Path, "add", "tracked")
				staged = git(t, a.Path, "rev-parse", ":tracked")
				must(t, os.WriteFile(filepath.Join(a.Path, "tracked"), []byte("a"), 0600))
				must(t, os.WriteFile(filepath.Join(a.Path, "ignored"), []byte("private"), 0600))
			}
			b, e := f.m.Archive(context.Background(), "source", 0, nil)
			must(t, e)
			if b.Workspace.Phase != "archived" {
				t.Fatal("not archived")
			}
			p, e := f.m.PlanReap(context.Background(), "source")
			must(t, e)
			if p.Version != workspacearchive.MissingPlanVersion || p.Missing == nil || p.Missing.Root.Absent != whole {
				t.Fatal("missing explicit tree/absence approval")
			}
			if !whole {
				found := false
				for _, v := range p.Tree.Entries {
					if v.Path == "a/tracked" {
						found = true
						if !v.Recoverable {
							t.Fatal("unchanged workfile must match old seal; index evidence remains distinct")
						}
					}
				}
				if !found {
					t.Fatal("dirty bytes missing from concrete review")
				}
			}
			raw, e := p.Bytes()
			must(t, e)
			p, e = workspacearchive.ReadPlan(raw)
			must(t, e)
			b, e = f.m.Reap(context.Background(), *p)
			must(t, e)
			if b.Workspace.Phase != "reaped" {
				t.Fatal("not reaped")
			}
			admin, e := f.s.WorkspaceRepair()
			must(t, e)
			must(t, admin.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
				r, e := history.Get(tx, strings.Repeat("b", 32))
				if e != nil {
					return e
				}
				raw, e := json.Marshal(r)
				if e != nil {
					return e
				}
				if string(raw) != string(f.history) || r.ExitCode != nil || !r.ReleasedAt.IsZero() {
					t.Fatal("archive fabricated/released unknown history")
				}
				if !runprotection.Runs(rows)[r.ID] {
					t.Fatal("reaped source history lost collection protection")
				}
				return nil
			}))

			if !whole {
				if len(p.Missing.Present) != 1 {
					t.Fatal("index-only evidence omitted from reviewed administration")
				}
				r := p.Missing.Present[0].Plan
				found := false
				for _, object := range r.Objects {
					if object.OID == staged {
						found = true
					}
				}
				if !found {
					t.Fatal("staged blob missing from retained object roots")
				}
				index, e := os.ReadFile(filepath.Join(r.Admin, "index"))
				must(t, e)
				var evidence struct{ Stage string }
				raw, e := os.ReadFile(r.Evidence)
				must(t, e)
				must(t, json.Unmarshal(raw, &evidence))
				copy, e := os.ReadFile(filepath.Join(filepath.Dir(r.Evidence), evidence.Stage, "admin", "index"))
				must(t, e)
				if string(copy) != string(index) {
					t.Fatal("retained index bytes differ")
				}
				if _, e = os.Lstat(filepath.Join(r.Admin, "locked")); e != nil {
					t.Fatal("owned present admin lock missing", e)
				}
			}
			for _, member := range f.p.Members {
				if member.Missing != nil {
					if _, e = os.Lstat(member.Missing.Admin); e != nil {
						t.Fatal("quarantine administration deleted", e)
					}
					if _, e = os.Lstat(member.Missing.Evidence); e != nil {
						t.Fatal("quarantine evidence deleted", e)
					}
				}
			}
			_, e = f.m.Reap(context.Background(), *p)
			must(t, e)
			must(t, os.MkdirAll(f.b.Path, 0700))
			sentinel := filepath.Join(f.b.Path, "returned")
			must(t, os.WriteFile(sentinel, []byte("preserve"), 0600))
			if _, e = f.m.Reap(context.Background(), *p); e == nil {
				t.Fatal("reaped replay accepted returned root")
			}
			if _, e = f.m.Archive(context.Background(), "source", 0, nil); e == nil {
				t.Fatal("archive replay accepted returned root")
			}
			if raw, e = os.ReadFile(sentinel); e != nil || string(raw) != "preserve" {
				t.Fatal("returned files changed")
			}
			if !whole {
				// Remove only this test's deliberately returned fixture after
				// proving both production replays preserved its bytes.
				must(t, os.Remove(sentinel))
				must(t, os.Remove(f.b.Path))
				r := p.Missing.Present[0].Plan
				git(t, r.Spec.Common, "gc", "--prune=now")
				if git(t, r.Spec.Common, "cat-file", "blob", staged) != "staged unknown work" {
					t.Fatal("staged blob lost after GC")
				}
				if git(t, r.Spec.Common, "--git-dir="+r.Admin, "rev-parse", ":tracked") != staged {
					t.Fatal("retained index changed")
				}
				if _, e = f.m.Reap(context.Background(), *p); e == nil || !strings.Contains(e.Error(), "reflog") {
					t.Fatal("external GC reflog mutation must refuse strict replay without deleting evidence", e)
				}
				if git(t, r.Spec.Common, "cat-file", "blob", staged) != "staged unknown work" {
					t.Fatal("refused replay lost staged blob")
				}

			}

		})
	}
}
func TestMissingReapRetriesPartialDeleteWithoutAdoptingReturnedMissingMember(t *testing.T) {
	f := makeMissing(t, false)
	_, e := f.m.Archive(context.Background(), "source", 0, nil)
	must(t, e)
	p, e := f.m.PlanReap(context.Background(), "source")
	must(t, e)
	f.m.AfterDelete = func() error { return errors.New("injected after-delete failure") }
	_, e = f.m.Reap(context.Background(), *p)
	if e == nil {
		t.Fatal("fault not reached")
	}
	missing := f.b.Workspace.Members[1].Path
	must(t, os.Mkdir(missing, 0700))
	sentinel := filepath.Join(missing, "new")
	must(t, os.WriteFile(sentinel, []byte("keep"), 0600))
	f.m.AfterDelete = nil
	if _, e = f.m.Reap(context.Background(), *p); e == nil {
		t.Fatal("retry adopted returned missing member")
	}
	if _, e = os.Stat(sentinel); e != nil {
		t.Fatal("returned evidence lost", e)
	}
	must(t, os.Remove(sentinel))
	must(t, os.Remove(missing))
	_, e = f.m.Reap(context.Background(), *p)
	must(t, e)
}

func TestMissingReapSQLFailureAfterRootUnlinkNeverAdoptsReturnedRoot(t *testing.T) {
	f := makeMissing(t, false)
	_, e := f.m.Archive(context.Background(), "source", 0, nil)
	must(t, e)
	p, e := f.m.PlanReap(context.Background(), "source")
	must(t, e)
	within := f.m.Within
	fail := true
	f.m.Within = func(fn func(workspace.Update) error) error {
		return within(func(update workspace.Update) error {
			return fn(func(change workspace.Change) error {
				return update(func(rows benchstate.Database, tx *sql.Tx) error {
					if e := change(rows, tx); e != nil {
						return e
					}
					if fail && rows["source"].Workspace.Phase == "reaped" {
						fail = false
						return errors.New("injected final SQL failure after unlink")
					}
					return nil
				})
			})
		})
	}
	_, e = f.m.Reap(context.Background(), *p)
	if e == nil || fail {
		t.Fatal("final SQL fault not reached", e)
	}
	if _, e = os.Lstat(f.b.Path); !os.IsNotExist(e) {
		t.Fatal("root not removed before failed save", e)
	}
	must(t, os.Mkdir(f.b.Path, 0700))
	if _, e = f.m.Reap(context.Background(), *p); e == nil {
		t.Fatal("returned empty root adopted after unlink")
	}
	if st, e := os.Lstat(f.b.Path); e != nil || !st.IsDir() {
		t.Fatal("returned empty root deleted", e)
	}
	must(t, os.Remove(f.b.Path))
	_, e = f.m.Reap(context.Background(), *p)
	must(t, e)
}

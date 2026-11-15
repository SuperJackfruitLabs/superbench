package workspacearchive

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

func transferFixture(t *testing.T) (*fixture, *model.Bench) {
	t.Helper()
	f := newFixture(t)
	ctx := context.Background()
	source := f.read(t)
	approval := transferApproval{}
	for i, m := range source.Workspace.Members {
		path, e := filepath.EvalSymlinks(m.Path)
		if e != nil {
			t.Fatal(e)
		}
		mirror, e := filepath.EvalSymlinks(m.Mirror)
		if e != nil {
			t.Fatal(e)
		}
		p, e := (worktreetransfer.Manager{Git: git}).Inspect(ctx, path, mirror, "refs/heads/"+m.Branch, source.Workspace.Checkpoint.Members[i].Commit)
		if e != nil {
			t.Fatal(e)
		}
		approval.Members = append(approval.Members, struct {
			Name     string
			Worktree worktreetransfer.Plan
		}{m.Name, p})
	}
	marker := &model.WorkspaceTransfer{ID: "handoff", Digest: strings.Repeat("d", 64), TargetKey: "destination", TargetID: strings.Repeat("e", 32), CreationID: "destination-create", Phase: "relinquishing"}
	raw, e := json.Marshal(approval)
	if e != nil {
		t.Fatal(e)
	}
	f.change(t, func(b *model.Bench) { b.Workspace.Transfer = marker })
	if e = f.manager.Within(func(update workspace.Update) error {
		return update(func(rows benchstate.Database, tx *sql.Tx) error {
			if e := reftransfer.Start(tx, reftransfer.Record{ID: marker.ID, SourceID: source.Workspace.ID, SourceKey: source.Key, TargetID: marker.TargetID, TargetKey: marker.TargetKey, CreationID: marker.CreationID, Digest: marker.Digest, Plan: raw, CreatedAt: time.Now().UTC(), Phase: "started"}); e != nil {
				return e
			}
			for _, member := range approval.Members {
				m := worktreetransfer.Manager{Git: git, Gate: func(context.Context, worktreetransfer.Plan) error {
					if !f.held {
						t.Fatal("transfer outside lock")
					}
					return nil
				}}
				if e := m.Apply(ctx, member.Worktree); e != nil {
					return e
				}
			}
			if e := reftransfer.Relinquish(tx, marker.ID, marker.Digest); e != nil {
				return e
			}
			target := workspace.Clone(source)
			target.Key = marker.TargetKey
			target.Path = filepath.Join(f.manager.Data, "benches", target.Key)
			target.ProjectID++
			target.Workspace.ID = marker.TargetID
			target.Workspace.CreationID = marker.CreationID
			target.Workspace.Phase = "materializing"
			target.Workspace.Checkpoint = nil
			target.Workspace.Preparation = nil
			target.Workspace.Transfer = nil
			target.Workspace.TransferredFrom = &model.WorkspaceTransferOrigin{ID: marker.ID, Digest: marker.Digest, SourceID: source.Workspace.ID, SourceKey: source.Key}
			target.Lease, e = model.NewLease("initialization")
			if e != nil {
				return e
			}
			target.Lease.Continuation, e = model.Nonce()
			if e != nil {
				return e
			}
			for i := range target.Workspace.Members {
				m := &target.Workspace.Members[i]
				m.Path = filepath.Join(target.Path, m.Name)
				m.Source = source.Workspace.Checkpoint.Members[i].Commit
				if e := os.MkdirAll(filepath.Dir(m.Path), 0700); e != nil {
					return e
				}
				if _, e := git(ctx, m.Mirror, "worktree", "add", m.Path, m.Branch); e != nil {
					return e
				}
			}
			rows[source.Key].Workspace.Transfer.Phase = "transferred"
			rows[target.Key] = target
			return reftransfer.Complete(tx, marker.ID, marker.Digest)
		})
	}); e != nil {
		t.Fatal(e)
	}
	db, e := statestoreOpen(f.manager.State)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	rows, e := benchstate.Load(db)
	if e != nil {
		t.Fatal(e)
	}
	return f, rows[marker.TargetKey]
}

// Keep the fixture's direct read separate from state mutations/transition checks.
func statestoreOpen(state string) (*sql.DB, error) {
	return statestore.Open(filepath.Join(state, "bench.db"), true)
}

func TestTransferredSourceArchivesAndReapsAfterDestinationAdvances(t *testing.T) {
	f, target := transferFixture(t)
	ctx := context.Background()
	before := workspace.Clone(target)
	for _, m := range target.Workspace.Members {
		if e := os.WriteFile(filepath.Join(m.Path, "README"), []byte("destination owns task ref "+m.Name), 0600); e != nil {
			t.Fatal(e)
		}
		gitOK(t, m.Path, "add", "README")
		gitOK(t, m.Path, "commit", "-m", "destination work")
		gitOK(t, m.Path, "push", "origin", m.Branch)
	}
	source := f.read(t)
	l := f.manager.lifecycle()
	if e := l.Confirm(ctx, source); e == nil {
		t.Fatal("ordinary confirmation accepted advanced task tips")
	}
	if e := l.ConfirmTransferred(ctx, source); e != nil {
		t.Fatal(e)
	}
	bytes := map[string]string{}
	refs := map[string]string{}
	for _, m := range target.Workspace.Members {
		raw, e := os.ReadFile(filepath.Join(m.Path, "README"))
		if e != nil {
			t.Fatal(e)
		}
		bytes[m.Name] = string(raw)
		refs[m.Name] = gitOK(t, m.Mirror, "rev-parse", "refs/heads/"+m.Branch)
	}
	ignored := filepath.Join(source.Workspace.Members[0].Path, "cache.out")
	if e := os.WriteFile(ignored, []byte("preserved private output"), 0600); e != nil {
		t.Fatal(e)
	}
	archived := f.archive(t)
	info, e := os.Stat(archived.Workspace.Members[0].Path)
	if e != nil || info.Mode().Perm()&0222 != 0 {
		t.Fatal("source not read-only", e)
	}
	plan, e := f.manager.PlanReap(ctx, source.Key)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, entry := range plan.Tree.Entries {
		if entry.Path == "a/cache.out" {
			found = true
			if entry.Recoverable {
				t.Fatal("ignored bytes called recoverable")
			}
		}
	}
	if !found {
		t.Fatal("ignored output absent from approval")
	}
	if _, e = f.manager.Reap(ctx, *plan); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Lstat(source.Path); !os.IsNotExist(e) {
		t.Fatal("source remains", e)
	}
	for _, m := range target.Workspace.Members {
		raw, e := os.ReadFile(filepath.Join(m.Path, "README"))
		if e != nil || string(raw) != bytes[m.Name] {
			t.Fatal("destination bytes changed", e)
		}
		if gitOK(t, m.Mirror, "rev-parse", "refs/heads/"+m.Branch) != refs[m.Name] {
			t.Fatal("destination ref changed")
		}
		if gitOK(t, m.Path, "symbolic-ref", "HEAD") != "refs/heads/"+m.Branch {
			t.Fatal("destination lost ownership")
		}
	}
	db, e := statestoreOpen(f.manager.State)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	rows, e := benchstate.Load(db)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(rows[target.Key], before) {
		t.Fatal("destination record changed")
	}
	if e = l.ConfirmTransferred(ctx, f.read(t)); e != nil {
		t.Fatal("reaped immutable evidence", e)
	}
}

func TestTransferredSourceArchiveRejectsChangedIdentityAndIncompleteTransfer(t *testing.T) {
	for _, kind := range []string{"locked", "root-output", "tracked", "registration", "gitfile", "checkpoint-pin"} {
		t.Run(kind, func(t *testing.T) {
			f, _ := transferFixture(t)
			source := f.read(t)
			member := source.Workspace.Members[1]
			switch kind {
			case "locked":
				gitOK(t, member.Mirror, "worktree", "lock", member.Path)
			case "root-output":
				if e := os.WriteFile(filepath.Join(source.Path, "unknown"), []byte("keep"), 0600); e != nil {
					t.Fatal(e)
				}
			case "tracked":
				if e := os.WriteFile(filepath.Join(member.Path, "README"), []byte("keep changed bytes"), 0600); e != nil {
					t.Fatal(e)
				}
			case "registration":
				admin := gitOK(t, member.Path, "rev-parse", "--absolute-git-dir")
				if e := os.Rename(admin, admin+"-original"); e != nil {
					t.Fatal(e)
				}
				if e := os.Mkdir(admin, 0700); e != nil {
					t.Fatal(e)
				}
			case "gitfile":
				path := filepath.Join(member.Path, ".git")
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(path, path+"-original"); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(path, raw, 0600); e != nil {
					t.Fatal(e)
				}
			case "checkpoint-pin":
				ref, err := checkpointset.PinRef(source.Workspace.ID, source.Workspace.Checkpoint.GenerationID, member.Name)
				if err != nil {
					t.Fatal(err)
				}
				gitOK(t, member.Mirror, "push", "origin", ":"+ref)
			}
			if _, e := f.manager.Archive(context.Background(), source.Key, 0, nil); e == nil {
				t.Fatal("changed source archived")
			}
			if f.read(t).Workspace.Archive != nil {
				t.Fatal("failed archive changed state")
			}
		})
	}
	f := newFixture(t)
	f.change(t, func(b *model.Bench) {
		b.Workspace.Transfer = &model.WorkspaceTransfer{ID: "handoff", Digest: strings.Repeat("d", 64), TargetKey: "target", TargetID: strings.Repeat("e", 32), CreationID: "target-create", Phase: "relinquishing"}
	})
	if _, e := f.manager.Archive(context.Background(), "card", 0, nil); e == nil {
		t.Fatal("relinquishing source archived")
	}
}

func TestTransferredReapRejectsReplacedOrLockedAdministration(t *testing.T) {
	for _, kind := range []string{"replaced", "locked"} {
		t.Run(kind, func(t *testing.T) {
			f, target := transferFixture(t)
			source := f.archive(t)
			plan, err := f.manager.PlanReap(context.Background(), source.Key)
			if err != nil {
				t.Fatal(err)
			}
			member := source.Workspace.Members[1]
			approved := plan.Registrations[1]
			if kind == "locked" {
				gitOK(t, member.Mirror, "worktree", "lock", member.Path)
			} else {
				saved := filepath.Join(f.manager.State, "saved-original-admin")
				if err = os.Rename(approved.Admin, saved); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(approved.Admin, 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"HEAD", "gitdir", "commondir", "index"} {
					raw, e := os.ReadFile(filepath.Join(saved, name))
					if e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(filepath.Join(approved.Admin, name), raw, 0600); e != nil {
						t.Fatal(e)
					}
				}
			}
			if _, err = f.manager.Reap(context.Background(), *plan); err == nil {
				t.Fatal("changed administration allowed deletion")
			}
			for _, m := range append(source.Workspace.Members, target.Workspace.Members...) {
				if _, err = os.Stat(filepath.Join(m.Path, "README")); err != nil {
					t.Fatal("refusal deleted work", err)
				}
			}
			if f.read(t).Workspace.Phase != "archived" {
				t.Fatal("refused reap changed phase")
			}
		})
	}
}

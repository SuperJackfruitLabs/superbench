// Package workspacerecreatetest drives a create on a reclaimed key through the
// real staging sequence and state validation, outside the workspace package,
// which is at its size bound. Only the Fresh helper is replaced.
package workspacerecreatetest

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gitcontrol.Command(context.Background(), dir, args...)
	cmd.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", args[0], err, out)
	}
}

type fixture struct {
	m       workspace.Manager
	spec    workspace.Spec
	fresh   []*model.Bench
	project uint32
}

func newFixture(t *testing.T) *fixture {
	root := t.TempDir()
	remote := filepath.Join(root, "a")
	if err := os.Mkdir(remote, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, remote, "init", "-b", "main")
	git(t, remote, "commit", "--allow-empty", "-m", "initial")
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	f := &fixture{spec: workspace.Spec{Key: "card", CheckpointRepository: "a", Repositories: []workspace.Repository{{Name: "a", Repository: remote}}}, project: 10000}
	f.m = workspace.Manager{State: state, Data: filepath.Join(root, "data"), Admit: func() error { return nil },
		Allocate: func(*model.Bench) error { t.Fatal("a Fresh create used the legacy allocator"); return nil },
		Git: func(ctx context.Context, dir string, args ...string) (string, error) {
			cmd := gitcontrol.Command(ctx, dir, args...)
			cmd.Env = gitcontrol.Environment(os.Environ())
			out, err := cmd.Output()
			return string(out), err
		},
		Fresh: func(_ context.Context, b *model.Bench) error {
			f.fresh = append(f.fresh, workspace.Clone(b))
			b.ProjectID, b.QuotaBytes, b.QuotaInodes = f.project, 1<<30, 1000
			f.project++
			return nil
		}}
	f.m.Within = func(fn func(workspace.Update) error) error {
		return statestore.Within(state, nil, func(s *statestore.Session) error {
			return fn(func(change workspace.Change) error {
				return s.Update(func(tx *sql.Tx) error {
					if err := benchstate.Migrate(tx, state, func(*model.Bench) error { return nil }); err != nil {
						return err
					}
					return benchstate.Update(tx, change)
				})
			})
		})
	}
	return f
}

func (f *fixture) read(t *testing.T) *model.Bench {
	t.Helper()
	var out *model.Bench
	if err := f.m.Within(func(update workspace.Update) error {
		return update(func(rows benchstate.Database, _ *sql.Tx) error { out = workspace.Clone(rows["card"]); return nil })
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// reclaim writes the staged first bench as its reclaimed record directly: a
// released, checkpointed bench whose tree is gone, as the reclaim leaves it.
func (f *fixture) reclaim(t *testing.T) *model.Bench {
	t.Helper()
	b := f.read(t)
	b.Lease, b.Workspace.IdleSince, b.Workspace.Phase, b.Workspace.Preparation = nil, "", "reclaimed", nil
	c := &model.WorkspaceCheckpoint{GenerationID: strings.Repeat("e", 32), Members: []model.WorkspaceCommit{{Name: "a", Commit: b.Workspace.Members[0].Source}}}
	manifest, err := workspacepolicy.Manifest(b, c)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(manifest)
	c.Digest, c.Seal = checkpointset.Digest(raw), strings.Repeat("f", 40)
	b.Workspace.Checkpoint = c
	if err = workspacepolicy.Validate(b); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(b)
	db, err := statestore.Open(filepath.Join(f.m.State, "bench.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("UPDATE benches SET record=? WHERE key='card'", string(record)); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestACreateOnAReclaimedKeyStartsItsNextGeneration(t *testing.T) {
	f := newFixture(t)
	first, err := f.m.Stage(context.Background(), "request1", f.spec)
	if err != nil || first.Workspace.Generation != 0 {
		t.Fatalf("first generation: %+v %v", first, err)
	}
	old := f.reclaim(t)
	// A replay of the reclaimed bench's own create returns it unchanged.
	if replay, err := f.m.Stage(context.Background(), "request1", f.spec); err != nil || replay.Workspace.Phase != "reclaimed" || !reflect.DeepEqual(f.read(t), old) || len(f.fresh) != 1 {
		t.Fatalf("original create replay: %+v %v", replay, err)
	}
	// A daemon without Fresh payload cannot make the next generation.
	legacy := f.m
	legacy.Fresh = nil
	if _, err := legacy.Stage(context.Background(), "request2", f.spec); err == nil || !reflect.DeepEqual(f.read(t), old) {
		t.Fatalf("legacy re-creation: %v", err)
	}
	second, err := f.m.Stage(context.Background(), "request2", f.spec)
	w := second.Workspace
	if err != nil || w.Phase != "initializing" || w.Generation != 2 || w.Previous != old.Workspace.ID || w.ID == old.Workspace.ID || w.CreationID != "request2" || second.ProjectID != 10001 {
		t.Fatalf("second generation: %+v %v", second, err)
	}
	// The helper saw the next generation's own identity before any allocation.
	if got := f.fresh[1]; len(f.fresh) != 2 || got.ProjectID != 0 || got.Workspace.Generation != 2 || got.Workspace.Previous != old.Workspace.ID || got.Workspace.ID != w.ID {
		t.Fatalf("helper input %+v", got.Workspace)
	}
	// The same create is an exact retry; any other create conflicts.
	if retry, err := f.m.Stage(context.Background(), "request2", f.spec); err != nil || !reflect.DeepEqual(retry, second) || len(f.fresh) != 2 {
		t.Fatalf("retry: %v %d", err, len(f.fresh))
	}
	for _, request := range []string{"request1", "request3"} {
		if _, err := f.m.Stage(context.Background(), request, f.spec); err == nil || !strings.Contains(err.Error(), "conflicts with an existing bench") {
			t.Errorf("%s on the second generation: %v", request, err)
		}
	}
	if !reflect.DeepEqual(f.read(t), second) {
		t.Fatal("a refused create changed the second generation")
	}
}

package workspacecompat

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/autoarchive"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := gitcontrol.Command(context.Background(), dir, args...)
	c.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", args[0], err, out)
	}
	return strings.TrimSpace(string(out))
}

func fixture(t *testing.T) (bench.Store, *model.Bench) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sb-compat-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data"), CgroupRoot: filepath.Join(root, "cgroups")}
	if err := os.Mkdir(store.CgroupRoot, 0700); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "remote")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "commit", "--allow-empty", "-m", "base")
	if _, err := store.CreateColdContext(context.Background(), "legacy", bench.Options{Repo: repo}, false); err != nil {
		t.Fatal(err)
	}
	groupRoot := filepath.Join(store.Data, "benches", "group")
	if err := os.Mkdir(groupRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(groupRoot, "preserve"), []byte("workspace contents"), 0600); err != nil {
		t.Fatal(err)
	}
	group := &model.Bench{Key: "group", Path: groupRoot, Lease: &model.Lease{Holder: "initialization", Token: strings.Repeat("a", 48), Continuation: strings.Repeat("b", 32)}, Workspace: &model.Workspace{ID: strings.Repeat("c", 32), CreationID: "creation-1", CheckpointRepository: "repo", Phase: "materializing", Members: []model.WorkspaceMember{{Name: "repo", Repository: repo, Branch: "bench/group", Base: "main", Path: filepath.Join(groupRoot, "repo"), Mirror: repository.Path(store.Data, repo), Source: runGit(t, repo, "rev-parse", "HEAD"), ObjectFormat: "sha1"}}}}
	raw, err := json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(store.State, "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO benches(key,record) VALUES(?,?)", group.Key, string(raw)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return store, group
}

func tree(t *testing.T, root string) map[string]any {
	t.Helper()
	out := map[string]any{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[name+":mode"] = info.Mode()
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[name] = sha256.Sum256(raw)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func unchanged(t *testing.T, store bench.Store, before map[string]any) {
	t.Helper()
	if after := tree(t, filepath.Dir(store.State)); !reflect.DeepEqual(before, after) {
		t.Fatal("refused scalar operation changed state, files, logs, locks, or backend directory")
	}
}

func TestScalarStatusRefusesAggregateWithoutPartialProjection(t *testing.T) {
	store, group := fixture(t)
	before := tree(t, filepath.Dir(store.State))
	for _, key := range []string{group.Key, ""} {
		states, err := store.Status(key)
		if !errors.Is(err, bench.ErrWorkspace) || states != nil {
			t.Fatalf("key %q misleading projection: %+v %v", key, states, err)
		}
		if err := store.RequireLegacy(key); !errors.Is(err, bench.ErrWorkspace) {
			t.Fatalf("key %q preflight: %v", key, err)
		}
	}
	states, err := store.Snapshot("")
	if err != nil || len(states) != 2 {
		t.Fatalf("maintenance snapshot: %+v %v", states, err)
	}
	if states[0].Workspace == nil || states[0].Lease == nil || states[0].Lease.Token != "" || states[0].Lease.Continuation != "" {
		t.Fatalf("workspace maintenance view lost identity/redaction: %+v", states[0])
	}
	legacy, err := store.Status("legacy")
	if err != nil || len(legacy) != 1 || legacy[0].Workspace != nil {
		t.Fatalf("legacy key alongside workspace: %+v %v", legacy, err)
	}
	if err := store.RequireLegacy("missing"); err != nil {
		t.Fatalf("missing key preflight: %v", err)
	}
	unchanged(t, store, before)
}

func TestScalarMutationsRefuseBeforeAnyEffects(t *testing.T) {
	store, group := fixture(t)
	// If admission is reached this malformed configuration fails differently.
	if err := os.WriteFile(filepath.Join(store.State, "host.json"), []byte("invalid host admission"), 0600); err != nil {
		t.Fatal(err)
	}
	// A mutation/session would attempt control reconciliation before loading its
	// records. Scalar refusal must precede even that recovery and schema barrier.
	controls := filepath.Join(store.State, "git-controls")
	if err := os.Mkdir(controls, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controls, strings.Repeat("d", 32)+".json"), []byte("unreconciled control intent"), 0600); err != nil {
		t.Fatal(err)
	}
	before := tree(t, filepath.Dir(store.State))
	check := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, bench.ErrWorkspace) {
			t.Fatalf("wanted aggregate refusal, got %v", err)
		}
		unchanged(t, store, before)
	}
	ctx := context.Background()
	key := group.Key
	token := group.Lease.Token
	operations := map[string]func() error{
		"run":          func() error { _, err := store.Run(ctx, key, "writer", []string{"true"}, false); return err },
		"setup":        func() error { _, err := store.Setup(ctx, key, "writer", false); return err },
		"lease":        func() error { _, err := store.LeaseContext(ctx, key, "writer"); return err },
		"repair_lease": func() error { _, err := store.RepairLease(key, "writer"); return err },
		"checkpoint":   func() error { _, err := store.Checkpoint(key, token); return err },
		"release":      func() error { return store.Release(key, token) },
		"stop":         func() error { _, err := store.Stop(key, token, false); return err },
		"stop_admin":   func() error { _, err := store.Stop(key, "", true); return err },
		"archive": func() error {
			_, err := store.ArchiveContext(ctx, key, time.Hour, func(context.Context, *bench.Bench) error { t.Fatal("archive callback executed"); return nil })
			return err
		},
		"plan_reap": func() error { _, err := store.PlanReap(key); return err },
		"reap":      func() error { _, err := store.Reap(bench.ReapPlan{Key: key}); return err },
	}
	for _, recovering := range []bool{false, true} {
		recovery := recovering
		name := "create"
		if recovery {
			name = "recover"
		}
		operations[name] = func() error {
			_, err := store.CreateContext(ctx, key, bench.Options{Repo: group.Workspace.Members[0].Repository}, recovery)
			return err
		}
		operations[name+"_cold"] = func() error {
			_, err := store.CreateColdContext(ctx, key, bench.Options{Repo: group.Workspace.Members[0].Repository}, recovery)
			return err
		}
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) { check(t, operation()) })
	}
	// Read the authoritative record separately to assert ownership is unchanged.
	states, err := store.Snapshot(key)
	if err != nil || len(states) != 1 || states[0].Lease == nil || states[0].Lease.Holder != "initialization" {
		t.Fatalf("initialization ownership lost: %+v %v", states, err)
	}
}

func TestAutoarchivePreservesAggregateOwnerWhileProcessingLegacy(t *testing.T) {
	store, group := fixture(t)
	before, err := store.Snapshot(group.Key)
	if err != nil {
		t.Fatal(err)
	}
	files := tree(t, group.Path)
	report, err := (&autoarchive.Manager{Store: store}).Tick(context.Background(), 10)
	if err != nil || len(report.Results) != 2 {
		t.Fatalf("mixed maintenance blocked: %+v %v", report, err)
	}
	if report.Results[0].Key != "group" || report.Results[0].Status != "leased" || report.Results[0].Error != "" {
		t.Fatalf("workspace archive attempted: %+v", report.Results[0])
	}
	after, err := store.Snapshot(group.Key)
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(files, tree(t, group.Path)) {
		t.Fatal("maintenance changed aggregate ownership or files", err)
	}
	if report.Results[1].Key != "legacy" || report.Results[1].Status != "not eligible" {
		t.Fatalf("legacy maintenance lost: %+v", report.Results[1])
	}
	if _, err := store.RepairLease("legacy", "writer"); err != nil {
		t.Fatalf("legacy acquisition beside aggregate: %v", err)
	}
}

func TestOldScalarSchemaStillMigratesThroughMutation(t *testing.T) {
	store, _ := fixture(t)
	db, err := sql.Open("sqlite", filepath.Join(store.State, "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM benches WHERE key='group'"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec("DROP TABLE workspace_operation_cancellations; DROP TABLE workspace_operation_runs; DROP TABLE workspace_operations; PRAGMA user_version=13"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.RequireLegacy("legacy"); err != nil {
		t.Fatalf("preflight prevented historical scalar migration: %v", err)
	}
	if _, err := store.RepairLease("legacy", "writer"); err != nil {
		t.Fatalf("ordinary scalar mutation failed migration: %v", err)
	}
	db, err = sql.Open("sqlite", filepath.Join(store.State, "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != benchstate.SchemaVersion {
		t.Fatalf("normal migration did not run: %d", version)
	}
	states, err := store.Status("legacy")
	if err != nil || len(states) != 1 || states[0].Lease == nil || states[0].Lease.Holder != "writer" {
		t.Fatalf("scalar lease not granted after migration: %+v %v", states, err)
	}
}

func TestLegacyPreflightDoesNotInitializeMissingState(t *testing.T) {
	root := t.TempDir()
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	before := tree(t, root)
	for _, key := range []string{"", "new-key"} {
		if err := store.RequireLegacy(key); err != nil {
			t.Fatalf("empty state preflight %q: %v", key, err)
		}
	}
	if after := tree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("legacy preflight initialized storage")
	}
}

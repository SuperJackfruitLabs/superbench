package workspaceflow

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitcontrol.Command(context.Background(), dir, args...)
	cmd.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", args[0], err, out)
	}
	return strings.TrimSpace(string(out))
}

func fixture(t *testing.T) (bench.Store, workspace.Spec, string) {
	t.Helper()
	root := t.TempDir()
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data"), CgroupRoot: filepath.Join(root, "unavailable-cgroups")}
	spec := workspace.Spec{Key: "card", CheckpointRepository: "a"}
	for _, name := range []string{"a", "b"} {
		remote := filepath.Join(root, "remote-"+name)
		if err := os.Mkdir(remote, 0700); err != nil {
			t.Fatal(err)
		}
		git(t, remote, "init", "-b", "main")
		if err := os.WriteFile(filepath.Join(remote, "source.txt"), []byte("source "+name), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, remote, "add", ".")
		git(t, remote, "commit", "-m", "base")
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: name, Repository: remote})
	}
	return store, spec, root
}

type entry struct {
	Mode   fs.FileMode
	Digest [32]byte
	Link   string
}

func tree(t *testing.T, root string) map[string]entry {
	t.Helper()
	result := map[string]entry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := entry{Mode: info.Mode()}
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			e.Digest = sha256.Sum256(raw)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			e.Link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[name] = e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestFacadeStagesWholeWorkspaceAndRetainsInitializationOwnership(t *testing.T) {
	store, spec, _ := fixture(t)
	result, err := store.StageWorkspaceContext(context.Background(), "creation-1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Workspace == nil || result.Workspace.Phase != "initializing" || len(result.Workspace.Members) != 2 {
		t.Fatalf("workspace not fully staged: %+v", result)
	}
	if result.Lease == nil || result.Lease.Holder != "initialization" || result.Lease.Token == "" || result.Lease.Continuation == "" {
		t.Fatal("initialization ownership was not retained")
	}
	if result.Repo != "" || result.Branch != "" || result.Mirror != "" || result.Checkpoint != "" {
		t.Fatal("aggregate presented misleading scalar metadata")
	}
	if result.Path != filepath.Join(store.Data, "benches", spec.Key) {
		t.Fatalf("wrong aggregate root: %s", result.Path)
	}
	for i, member := range result.Workspace.Members {
		r := spec.Repositories[i]
		if member.Name != r.Name || member.Repository != r.Repository || member.Path != filepath.Join(result.Path, r.Name) || member.Branch != "bench/"+spec.Key || member.Base != "main" {
			t.Fatalf("member identity differs: %+v", member)
		}
		want := git(t, r.Repository, "rev-parse", "HEAD")
		if member.Source != want || git(t, member.Path, "rev-parse", "HEAD") != want {
			t.Fatalf("member %s differs from pinned source", member.Name)
		}
		if git(t, member.Path, "symbolic-ref", "HEAD") != "refs/heads/"+member.Branch {
			t.Fatalf("member %s has wrong task branch", member.Name)
		}
		if got := git(t, member.Path, "status", "--porcelain", "--untracked-files=all"); got != "" {
			t.Fatalf("member %s not clean: %s", member.Name, got)
		}
		if raw, err := os.ReadFile(filepath.Join(member.Path, "source.txt")); err != nil || string(raw) != "source "+member.Name {
			t.Fatalf("member source missing: %v", err)
		}
	}
	snapshots, err := store.Snapshot(spec.Key)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshot failed: %+v %v", snapshots, err)
	}
	snapshot := snapshots[0]
	if snapshot.Workspace == nil || snapshot.Workspace.ID != result.Workspace.ID || snapshot.Lease == nil || snapshot.Lease.Holder != "initialization" || snapshot.Lease.Token != "" || snapshot.Lease.Continuation != "" {
		t.Fatal("snapshot lost aggregate identity or disclosed ownership capabilities")
	}
	if states, err := store.Status(spec.Key); !errors.Is(err, bench.ErrWorkspace) || states != nil {
		t.Fatalf("scalar status projected workspace: %+v %v", states, err)
	}
	if _, err := store.RepairLease(spec.Key, "other"); !errors.Is(err, bench.ErrWorkspace) {
		t.Fatalf("scalar acquisition bypassed workspace ownership: %v", err)
	}
}

func TestFacadeRetryPreservesExactIdentityAndPinnedVector(t *testing.T) {
	store, spec, root := fixture(t)
	first, err := store.StageWorkspaceContext(context.Background(), "creation-1", spec)
	if err != nil {
		t.Fatal(err)
	}
	// A completed staging retry must not select fresh remote tips or repeat the
	// preparation/materialization stage under the same creation identity.
	for _, r := range spec.Repositories {
		git(t, r.Repository, "commit", "--allow-empty", "-m", "later remote tip")
	}
	before := tree(t, root)
	second, err := store.StageWorkspaceContext(context.Background(), "creation-1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("idempotent retry changed immutable vector, quota, ownership, or phase")
	}
	if after := tree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("idempotent staging retry changed filesystem state")
	}
	for _, member := range second.Workspace.Members {
		if got := git(t, member.Path, "rev-parse", "HEAD"); got != member.Source {
			t.Fatal("retry followed a moving remote tip")
		}
	}
}

func TestFacadeConflictPreservesExistingWorkspaceAndFiles(t *testing.T) {
	store, spec, root := fixture(t)
	first, err := store.StageWorkspaceContext(context.Background(), "creation-1", spec)
	if err != nil {
		t.Fatal(err)
	}
	// User output after staging must survive conflicts without cleanup or reset.
	sentinel := filepath.Join(first.Workspace.Members[0].Path, "untracked-user-work")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	before := tree(t, root)
	for _, change := range []struct {
		name, id string
		change   func(*workspace.Spec)
	}{
		{name: "different creation ID", id: "creation-2", change: func(*workspace.Spec) {}},
		{name: "changed member branch", id: "creation-1", change: func(s *workspace.Spec) { s.Repositories[1].Branch = "other/task" }},
		{name: "changed checkpoint repository", id: "creation-1", change: func(s *workspace.Spec) { s.CheckpointRepository = "b" }},
		{name: "changed explicit quota", id: "creation-1", change: func(s *workspace.Spec) { s.QuotaBytes = 1 << 30 }},
	} {
		t.Run(change.name, func(t *testing.T) {
			request := spec
			request.Repositories = append([]workspace.Repository(nil), spec.Repositories...)
			change.change(&request)
			if _, err := store.StageWorkspaceContext(context.Background(), change.id, request); err == nil {
				t.Fatal("conflicting request accepted")
			}
			if after := tree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("conflict changed existing work or authority")
			}
		})
	}
	snapshot, err := store.Snapshot(spec.Key)
	if err != nil || len(snapshot) != 1 || snapshot[0].Workspace.ID != first.Workspace.ID || snapshot[0].Lease == nil || snapshot[0].Lease.Holder != "initialization" {
		t.Fatalf("conflict lost initialized identity: %+v %v", snapshot, err)
	}
}

func TestFacadeCanceledAndInvalidRequestsHaveNoStorageEffects(t *testing.T) {
	store, spec, root := fixture(t)
	before := tree(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := store.StageWorkspaceContext(ctx, "creation-1", spec); !errors.Is(err, context.Canceled) || result != nil {
		t.Fatalf("canceled staging: %+v %v", result, err)
	}
	for _, change := range []struct {
		id     string
		change func(*workspace.Spec)
	}{
		{id: "invalid/id", change: func(*workspace.Spec) {}},
		{id: "creation-1", change: func(s *workspace.Spec) { s.Key = "../escape" }},
		{id: "creation-1", change: func(s *workspace.Spec) { s.Repositories[1].Name = s.Repositories[0].Name }},
		{id: "creation-1", change: func(s *workspace.Spec) { s.Repositories[1].Branch = "refs/heads/main" }},
		{id: "creation-1", change: func(s *workspace.Spec) { s.QuotaBytes = 1025 }},
	} {
		request := spec
		request.Repositories = append([]workspace.Repository(nil), spec.Repositories...)
		change.change(&request)
		if result, err := store.StageWorkspaceContext(context.Background(), change.id, request); err == nil || result != nil {
			t.Fatalf("invalid request staged: %+v %v", result, err)
		}
	}
	if after := tree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("invalid/canceled request created state or changed source data")
	}
	for _, path := range []string{store.State, store.Data} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("invalid/canceled request initialized %s: %v", path, err)
		}
	}
}

func TestLegacyEffectCallbackHoldsStateLockAcrossKindCheck(t *testing.T) {
	store, spec, _ := fixture(t)
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := store.WithLegacy(spec.Key, func() error {
		called = true
		// A separate open description must not obtain an exclusive mutation lock
		// while the external effect callback still holds its shared kind-check lock.
		f, err := os.OpenFile(filepath.Join(store.State, "lock"), os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			t.Fatal("external effect callback ran without the shared state lock")
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			t.Fatalf("unexpected lock error: %v", err)
		}
		return nil
	}); err != nil || !called {
		t.Fatalf("missing-key effect callback failed: %v", err)
	}
	// Once the earlier effect releases its shared lock, a real aggregate stage
	// commits normally. Any later scalar effect must be refused before callback.
	if _, err := store.StageWorkspaceContext(context.Background(), "creation-1", spec); err != nil {
		t.Fatal(err)
	}
	before := tree(t, filepath.Dir(store.State))
	if err := store.WithLegacy(spec.Key, func() error { t.Fatal("effect ran after aggregate commit"); return nil }); !errors.Is(err, bench.ErrWorkspace) {
		t.Fatalf("aggregate effect preflight: %v", err)
	}
	if after := tree(t, filepath.Dir(store.State)); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected external effect changed aggregate/state")
	}
}

func TestMissingScalarRunRefusesBeforeBackendScopeAllocation(t *testing.T) {
	store, _, root := fixture(t)
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	// This is deliberately not a usable cgroup root. Creating or fencing a scope
	// before the under-lock lookup would encounter the backend instead of the
	// authoritative not-found result.
	if err := os.Mkdir(store.CgroupRoot, 0700); err != nil {
		t.Fatal(err)
	}
	before := tree(t, root)
	if result, err := store.Run(context.Background(), "missing", "writer", []string{"true"}, false); err == nil || execution.Supported && !errors.Is(err, bench.ErrNotFound) || result != nil {
		t.Fatalf("backend used before record lookup: %+v %v", result, err)
	}
	if after := tree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("missing scalar run changed state or created a backend scope/log")
	}
}

//go:build linux

package registrationobserve

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

// Real lifecycle reads must work beneath the retained host lock without
// migrating the database or creating a journal that invalidates the observation.
func TestHeldStateAllowsActualOwnerAndSnapshotReadsWithoutWrites(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v: %s", err, out)
		}
	}
	remote, seed := filepath.Join(root, "remote.git"), filepath.Join(root, "seed")
	git(root, "init", "--bare", "--initial-branch=main", remote)
	git(root, "init", "--initial-branch=main", seed)
	if err := os.WriteFile(filepath.Join(seed, "tracked"), []byte("retained source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(seed, "add", "tracked")
	git(seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "seed")
	git(seed, "push", remote, "main")
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	owner, err := store.StageWorkspaceContext(context.Background(), "observe-create", workspace.Spec{Key: "card", CheckpointRepository: "source", Repositories: []workspace.Repository{{Name: "source", Repository: remote}}})
	if err != nil {
		t.Fatal(err)
	}
	if owner.Lease == nil {
		t.Fatal("staging did not retain initialization owner")
	}
	before, err := store.Snapshot(owner.Key)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(store.State, "bench.db")
	original, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := os.ReadDir(store.State)
	if err != nil {
		t.Fatal(err)
	}
	held, err := holdState(context.Background(), store.State)
	if err != nil {
		t.Fatal(err)
	}
	defer held.close()
	if err = store.CheckWorkspaceOwner(owner.Key, owner.Lease.Token, owner.Lease.Holder); err != nil {
		t.Fatal(err)
	}
	after, err := store.Snapshot(owner.Key)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("read changed retained owner: %v", err)
	}
	if err = held.recheck(); err != nil {
		t.Fatalf("actual read invalidated held observation: %v", err)
	}
	current, err := os.ReadFile(dbPath)
	if err != nil || !bytes.Equal(original, current) {
		t.Fatalf("read changed database bytes: %v", err)
	}
	entries, err := os.ReadDir(store.State)
	if err != nil || len(entries) != len(inventory) {
		t.Fatalf("read changed state inventory: %v", err)
	}
	for i := range entries {
		if entries[i].Name() != inventory[i].Name() {
			t.Fatal("read created or removed a state entry")
		}
	}
}

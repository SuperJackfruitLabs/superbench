package workspaceflow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

func TestInitializeWorkspaceConfirmsWholeGenesisBeforeReadyAndReplays(t *testing.T) {
	store, spec, _ := fixture(t)
	ready, err := store.InitializeWorkspaceContext(context.Background(), "warm-create1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Lease != nil || ready.Workspace.Phase != "ready" || ready.Workspace.Pending != nil || ready.Workspace.Checkpoint == nil || len(ready.Workspace.Preparation) != 2 {
		t.Fatal("initialization did not publish a whole prepared checkpoint", ready)
	}
	manifest, err := workspacepolicy.Manifest(ready, ready.Workspace.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil || checkpointset.Digest(raw) != ready.Workspace.Checkpoint.Digest {
		t.Fatal("checkpoint digest does not bind complete vector", err)
	}
	for i, member := range ready.Workspace.Members {
		for _, ref := range []string{"refs/heads/" + member.Branch, manifest.Members[i].PinRef} {
			if commit := git(t, member.Repository, "rev-parse", "--verify", ref); commit != manifest.Members[i].Commit {
				t.Fatal("remote member checkpoint not confirmed", member.Name, ref)
			}
		}
	}
	meta, _ := checkpointset.MetadataRef(ready.Workspace.ID)
	if commit := git(t, ready.Workspace.Members[0].Repository, "rev-parse", "--verify", meta); commit != ready.Workspace.Checkpoint.Seal {
		t.Fatal("genesis seal not published")
	}
	before := tree(t, filepath.Dir(store.State))
	replay, err := store.InitializeWorkspaceContext(context.Background(), "warm-create1", spec)
	if err != nil || !reflect.DeepEqual(ready, replay) {
		t.Fatal("warm create retry changed the completed workspace", err)
	}
	if after := tree(t, filepath.Dir(store.State)); !reflect.DeepEqual(before, after) {
		t.Fatal("warm create replay changed authoritative files")
	}
}

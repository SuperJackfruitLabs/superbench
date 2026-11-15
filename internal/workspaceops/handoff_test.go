package workspaceops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSelectedSetupWithoutOtherProofRetainsNewOwner(t *testing.T) {
	m, r := fixture(t)
	ready := create(t, m, r)
	checkpoint := ready.Workspace.Workspace.Checkpoint
	setup := request("setup")
	setup.RepositoryName = "api"
	out, err := m.Execute(context.Background(), setup, 42)
	if !errors.Is(err, ErrConflict) || out == nil || out.GrantToken == "" || out.Workspace.Lease == nil {
		t.Fatal("partial setup falsely completed or lost its new owner", err)
	}
	current := snapshot(t, m)[0]
	if len(current.Workspace.Preparation) != 1 || current.Workspace.Preparation[0].Name != "api" || !reflect.DeepEqual(current.Workspace.Checkpoint, checkpoint) || current.Workspace.Pending != nil {
		t.Fatal("partial setup published a checkpoint or fabricated other proofs")
	}
	if err := m.Store.CheckWorkspaceOwner("card", out.GrantToken, "worker"); err != nil {
		t.Fatal("retained grant does not own the partial setup", err)
	}
}

func TestSelectedSetupRequiresCurrentUnselectedInputsAtBothBoundaries(t *testing.T) {
	for _, timing := range []string{"before-checkpoint", "during-checkpoint"} {
		t.Run(timing, func(t *testing.T) {
			m, r := fixture(t)
			for _, repo := range r.Repositories {
				if err := os.WriteFile(filepath.Join(repo.Repository, "package.json"), []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
				git(t, repo.Repository, "add", ".")
				git(t, repo.Repository, "commit", "-m", "preparation input")
			}
			create(t, m, r)
			held, err := m.Execute(context.Background(), request("lease"), 42)
			if err != nil {
				t.Fatal(err)
			}
			current := snapshot(t, m)[0]
			oldSeal := current.Workspace.Checkpoint.Seal
			web := current.Workspace.Members[1].Path
			input := filepath.Join(web, "package.json")
			// Git-clean is deliberately insufficient to prove current setup inputs.
			git(t, web, "update-index", "--assume-unchanged", "package.json")
			if timing == "before-checkpoint" {
				if err := os.WriteFile(input, []byte("{\"changed\":true}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				git(t, current.Workspace.Members[0].Path, "commit", "--allow-empty", "-m", "new checkpoint vector")
				hook := filepath.Join(r.Repositories[0].Repository, ".git", "hooks", "pre-receive")
				if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf '%s\\n' '{\"changed\":true}' > '"+input+"'\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			setup := request("setup")
			setup.RepositoryName, setup.LeaseToken = "api", held.GrantToken
			out, err := m.Execute(context.Background(), setup, 42)
			if !errors.Is(err, ErrConflict) || out == nil || out.GrantToken != "" || out.Workspace.Lease == nil {
				t.Fatal("stale unselected proof completed setup or reexported supplied grant", err)
			}
			if err := m.Store.CheckWorkspaceOwner("card", held.GrantToken, "worker"); err != nil {
				t.Fatal("refusal lost the original owner", err)
			}
			after := snapshot(t, m)[0]
			if timing == "before-checkpoint" && after.Workspace.Checkpoint.Seal != oldSeal {
				t.Fatal("preflight proof failure published a checkpoint")
			}
			if timing == "during-checkpoint" && (after.Workspace.Checkpoint.Seal == oldSeal || after.Workspace.Pending != nil) {
				t.Fatal("test did not reach the independent post-publication release gate")
			}
			if git(t, web, "status", "--porcelain") != "" {
				t.Fatal("fixture must remain Git-clean")
			}
		})
	}
}

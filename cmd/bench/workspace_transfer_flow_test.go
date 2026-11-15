package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

func TestWorkspaceTransferCLIReviewedOriginalRefRoundTrip(t *testing.T) {
	store, members := workspaceFixture(t)
	physical, err := filepath.EvalSymlinks(filepath.Dir(store.State))
	if err != nil {
		t.Fatal(err)
	}
	store.State = filepath.Join(physical, filepath.Base(store.State))
	store.Data = filepath.Join(physical, filepath.Base(store.Data))
	created := localCreate(t, store, members)
	before, err := store.Snapshot("card")
	if err != nil || len(before) != 1 {
		t.Fatal(err)
	}
	out, err := localCLI(t, store, "inspect-transfer", "--target", "destination", "--transfer-id", "transfer-cli-1", "--quota-bytes", "0", "card")
	if err != nil {
		t.Fatal("transfer inspection failed", err, string(out))
	}
	plan, err := workspacetransfer.ParsePlan(out)
	if err != nil || plan.SourceKey != "card" || plan.TargetKey != "destination" || plan.ID != "transfer-cli-1" || plan.SourceID != created.Workspace.BenchID || len(out) > workspacetransfer.MaxPlan {
		t.Fatal("inspection did not emit its canonical bounded approval", err)
	}
	path := filepath.Join(t.TempDir(), "transfer-plan.json")
	if err := os.WriteFile(path, out, 0600); err != nil {
		t.Fatal(err)
	}
	compact := bytes.TrimSpace(out)
	duplicate := append(append([]byte(nil), compact[:len(compact)-1]...), []byte(`,"Digest":"`+plan.Digest+`"}`)...)
	duplicatePath := filepath.Join(filepath.Dir(path), "duplicate-plan.json")
	if err := os.WriteFile(duplicatePath, duplicate, 0600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct{ path, source, approval string }{{path, "other-source", plan.Digest}, {path, "card", strings.Repeat("0", 64)}, {duplicatePath, "card", plan.Digest}} {
		out, err := localCLI(t, store, "begin-transfer", "--plan", invalid.path, "--approve", invalid.approval, invalid.source)
		if err == nil || len(out) != 0 {
			t.Fatal("inexact transfer approval reached manager", err)
		}
		after, err := store.Snapshot("card")
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("invalid approval changed source ownership", err)
		}
	}
	var begun workspacetransfer.Result
	for i := 0; i < 2; i++ {
		out, err = localCLI(t, store, "begin-transfer", "--plan", path, "--approve", plan.Digest, "card")
		if err != nil {
			t.Fatal("reviewed begin failed", err, string(out))
		}
		var replay workspacetransfer.Result
		if err := json.Unmarshal(out, &replay); err != nil || replay.Phase != "started" || replay.GrantToken != "" || replay.RootPath != "" {
			t.Fatal("begin exported execution authority or claimed completion", err)
		}
		if i > 0 && !reflect.DeepEqual(begun, replay) {
			t.Fatal("same reviewed begin changed transfer identity")
		}
		begun = replay
	}
	marked, err := store.Snapshot("card")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := localCLI(t, store, "continue-transfer", "--transfer-id", plan.ID, "other-source"); err == nil || len(out) != 0 {
		t.Fatal("different CLI source resumed the stored transfer", err)
	}
	afterWrongSource, err := store.Snapshot("card")
	if err != nil || !reflect.DeepEqual(marked, afterWrongSource) {
		t.Fatal("wrong-source continuation changed source marker", err)
	}
	if _, err := os.Lstat(filepath.Join(store.Data, "benches", plan.TargetKey)); !os.IsNotExist(err) {
		t.Fatal("wrong-source continuation materialized target", err)
	}
	for _, member := range before[0].Workspace.Members {
		if branch := cliGit(t, member.Path, "symbolic-ref", "HEAD"); branch != "refs/heads/"+member.Branch {
			t.Fatal("wrong-source continuation detached the original worktree")
		}
	}
	// Refuse only the destination's final seal. The CLI must still report the
	// authorized retained initialization owner after the source relinquishes.
	hook := filepath.Join(plan.Repositories[0].Repository, ".git", "hooks", "pre-receive")
	body := "#!/bin/sh\nwhile read old new ref; do\n if [ \"$ref\" = 'refs/heads/superbench/checkpoints/" + plan.TargetID + "' ]; then exit 1; fi\ndone\nexit 0\n"
	if err := os.WriteFile(hook, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	out, err = localCLI(t, store, "continue-transfer", "--transfer-id", plan.ID, "card")
	if err == nil || len(out) == 0 {
		t.Fatal("rejected destination seal lost its retained failure result", err)
	}
	var retained workspacetransfer.Result
	if err := json.Unmarshal(out, &retained); err != nil || retained.GrantToken == "" || retained.RootPath != filepath.Join(store.Data, "benches", plan.TargetKey) || retained.Phase == "completed" {
		t.Fatal("failure result omitted its authorized destination capability", err)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	var completed workspacetransfer.Result
	for i := 0; i < 2; i++ {
		out, err = localCLI(t, store, "continue-transfer", "--transfer-id", plan.ID, "card")
		if err != nil {
			t.Fatal("approved continuation failed", err, string(out))
		}
		var replay workspacetransfer.Result
		if err := json.Unmarshal(out, &replay); err != nil || replay.Version != workspacetransfer.Version || replay.Phase != "completed" || replay.GrantToken != "" || replay.SourceKey != "card" || replay.TargetKey != plan.TargetKey || replay.TargetID != plan.TargetID || replay.RootPath != filepath.Join(store.Data, "benches", plan.TargetKey) || len(out) > workspacetransfer.MaxResult {
			t.Fatal("transfer result lost reviewed identity or exported obsolete grant", err)
		}
		if i > 0 && !reflect.DeepEqual(completed, replay) {
			t.Fatal("completed transfer replay changed result")
		}
		completed = replay
	}
	sourceRows, err := store.Snapshot("card")
	if err != nil || len(sourceRows) != 1 || sourceRows[0].Lease != nil || sourceRows[0].Workspace.Transfer == nil || sourceRows[0].Workspace.Transfer.Phase != "transferred" {
		t.Fatal("source was not permanently relinquished", err)
	}
	targetRows, err := store.Snapshot(plan.TargetKey)
	if err != nil || len(targetRows) != 1 || targetRows[0].Lease != nil || targetRows[0].Workspace.Phase != "ready" || targetRows[0].Workspace.ID != plan.TargetID || targetRows[0].Workspace.ID == sourceRows[0].Workspace.ID || targetRows[0].Workspace.Checkpoint == nil || len(targetRows[0].Workspace.Preparation) != 2 {
		t.Fatal("fresh destination lacks full warm readiness", err)
	}
	for i, member := range sourceRows[0].Workspace.Members {
		if cliGit(t, member.Path, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" {
			t.Fatal("source still owns the original task branch")
		}
		if got := cliGit(t, member.Path, "rev-parse", "HEAD"); got != plan.Checkpoint.Members[i].Commit {
			t.Fatal("source HEAD moved during original-ref handoff")
		}
		if raw, err := os.ReadFile(filepath.Join(member.Path, "source.txt")); err != nil || string(raw) != member.Name {
			t.Fatal("source files changed during transfer", err)
		}
		if branch := cliGit(t, targetRows[0].Workspace.Members[i].Path, "symbolic-ref", "HEAD"); branch != "refs/heads/"+member.Branch {
			t.Fatal("destination did not acquire the original task ref")
		}
	}
}

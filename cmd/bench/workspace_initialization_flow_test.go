package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
)

func TestWorkspaceInitializationCLIReviewedClaimAndWarmCompletion(t *testing.T) {
	store, membersFile := workspaceFixture(t)
	physicalRoot, err := filepath.EvalSymlinks(filepath.Dir(store.State))
	if err != nil {
		t.Fatal(err)
	}
	store.State = filepath.Join(physicalRoot, filepath.Base(store.State))
	raw, err := os.ReadFile(membersFile)
	if err != nil {
		t.Fatal(err)
	}
	var members []protocolv2.Repository
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatal(err)
	}
	spec := workspace.Spec{Key: "card", CheckpointRepository: "api"}
	for _, member := range members {
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: member.Name, Repository: member.Repository})
	}
	staged, err := store.StageWorkspaceContext(context.Background(), "stopped-create", spec)
	if err != nil {
		t.Fatal(err)
	}
	oldToken, oldContinuation := staged.Lease.Token, staged.Lease.Continuation
	stopped, err := store.StopWorkspace("card", oldToken, true)
	if err != nil || stopped == nil || !stopped.Lease.StopRequested || stopped.Lease.Continuation != "" {
		t.Fatal("fixture did not revoke initialization", err)
	}
	out, err := localCLI(t, store, "inspect-initialization", "card")
	if err != nil {
		t.Fatal("inspect stopped initialization", err, string(out))
	}
	plan, err := workspacerepair.ParsePlan(out)
	if err != nil || plan.Key != "card" || len(out) > workspacerepair.MaxPlan {
		t.Fatal("CLI did not produce exact bounded plan", err)
	}
	if bytes.Contains(out, []byte(oldToken)) || bytes.Contains(out, []byte(oldContinuation)) {
		t.Fatal("inspection exported initialization authority")
	}
	path := filepath.Join(t.TempDir(), "repair-plan.json")
	if err := os.WriteFile(path, out, 0600); err != nil {
		t.Fatal(err)
	}
	beforeClaim, err := store.Snapshot("card")
	if err != nil {
		t.Fatal(err)
	}
	// A duplicate carrying the same correct digest decodes to the valid Plan;
	// canonical parsing must still refuse it before a claim can rotate ownership.
	compact := bytes.TrimSpace(out)
	duplicate := append(append([]byte(nil), compact[:len(compact)-1]...), []byte(`,"Digest":"`+plan.Digest+`"}`)...)
	duplicatePath := filepath.Join(filepath.Dir(path), "duplicate-plan.json")
	if err := os.WriteFile(duplicatePath, duplicate, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := localCLI(t, store, "claim-initialization", "--plan", duplicatePath, "--approve", plan.Digest, "--repair-id", "repair-cli-1", "card"); err == nil || len(out) != 0 {
		t.Fatal("duplicate field in otherwise valid approval reached claim", err)
	}
	for _, wrong := range []struct{ key, approval string }{{"different", plan.Digest}, {"card", strings.Repeat("0", 64)}} {
		out, err := localCLI(t, store, "claim-initialization", "--plan", path, "--approve", wrong.approval, "--repair-id", "repair-cli-1", wrong.key)
		if err == nil || len(out) != 0 {
			t.Fatal("CLI accepted a different plan/key approval", err)
		}
		after, err := store.Snapshot("card")
		if err != nil || !reflect.DeepEqual(beforeClaim, after) {
			t.Fatal("wrong approval changed stopped initialization", err)
		}
	}
	var claim workspacerepair.Result
	for i := 0; i < 2; i++ {
		out, err = localCLI(t, store, "claim-initialization", "--plan", path, "--approve", plan.Digest, "--repair-id", "repair-cli-1", "card")
		if err != nil {
			t.Fatal("explicit repair claim", err, string(out))
		}
		var next workspacerepair.Result
		if err := json.Unmarshal(out, &next); err != nil || next.GrantToken == "" || next.GrantToken == oldToken || next.RootPath != staged.Path || len(out) > workspacerepair.MaxResult {
			t.Fatal("claim omitted fresh authorized grant", err)
		}
		if i != 0 && !reflect.DeepEqual(claim, next) {
			t.Fatal("same approved repair ID rotated/rebound its grant")
		}
		claim = next
	}
	for _, wrong := range []struct{ id, token string }{{"repair-cli-wrong", claim.GrantToken}, {"repair-cli-1", oldToken}} {
		out, err := localCLI(t, store, "continue-initialization", "--repair-id", wrong.id, "--token", wrong.token, "card")
		if err == nil || len(out) != 0 {
			t.Fatal("wrong repair epoch/capability resumed initialization", err)
		}
	}
	dirtyPath := filepath.Join(staged.Path, "api", "source.txt")
	if err := os.WriteFile(dirtyPath, []byte("preserve this work"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = localCLI(t, store, "continue-initialization", "--repair-id", "repair-cli-1", "--token", claim.GrantToken, "card")
	if err == nil || len(out) == 0 {
		t.Fatal("dirty completion omitted its authorized retained result", err)
	}
	var retained workspacerepair.Result
	if err := json.Unmarshal(out, &retained); err != nil || retained.GrantToken != "" || retained.RootPath != staged.Path || bytes.Contains(out, []byte(claim.GrantToken)) {
		t.Fatal("failed continuation lost authorized root or exported supplied token", err)
	}
	if raw, err := os.ReadFile(dirtyPath); err != nil || string(raw) != "preserve this work" {
		t.Fatal("failed warm completion overwrote dirty work", err)
	}
	if err := os.WriteFile(dirtyPath, []byte("api"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = localCLI(t, store, "continue-initialization", "--repair-id", "repair-cli-1", "--token", claim.GrantToken, "card")
	if err != nil {
		t.Fatal("explicit repair failed warm initialization", err, string(out))
	}
	var finished workspacerepair.Result
	if err := json.Unmarshal(out, &finished); err != nil || finished.GrantToken != "" || finished.Phase != "ready" || bytes.Contains(out, []byte(claim.GrantToken)) {
		t.Fatal("completion re-exported supplied token or claimed wrong phase", err)
	}
	rows, err := store.Snapshot("card")
	if err != nil || len(rows) != 1 || rows[0].Lease != nil || rows[0].Workspace.Checkpoint == nil || len(rows[0].Workspace.Preparation) != 2 {
		t.Fatal("warm initialization lacked complete checkpoint/proofs", err)
	}
	ready := rows[0]
	for _, member := range ready.Workspace.Members {
		if raw, err := os.ReadFile(filepath.Join(member.Path, "source.txt")); err != nil || string(raw) != member.Name {
			t.Fatal("repair changed member files", err)
		}
	}
	out, err = localCLI(t, store, "claim-initialization", "--plan", path, "--approve", plan.Digest, "--repair-id", "repair-cli-1", "card")
	if err != nil {
		t.Fatal("completed repair claim could not reconcile", err)
	}
	var replay workspacerepair.Result
	if err := json.Unmarshal(out, &replay); err != nil || replay.GrantToken != "" {
		t.Fatal("completed repair claim re-exported obsolete grant", err)
	}
}

func TestWorkspaceInitializationResultBoundBeforeStdout(t *testing.T) {
	// A closed DTO can still exceed the transport cap after JSON escapes grow.
	result := &workspacerepair.Result{Key: "card", RootPath: strings.Repeat("<", workspacerepair.MaxResult/6)}
	var stdout bytes.Buffer
	if err := writeInitializationResult(&stdout, result); err == nil || stdout.Len() != 0 {
		t.Fatal("oversized serialized repair result partially reached stdout", err)
	}
}

func TestWorkspaceOrdinaryAmbientTokenAfterSafeFlagParsing(t *testing.T) {
	store, members := workspaceFixture(t)
	localCreate(t, store, members)
	out, err := localCLI(t, store, "lease", "--holder", "ambient-owner", "--request-id", "ambient-lease", "card")
	if err != nil {
		t.Fatal(err, string(out))
	}
	var leased struct{ Result protocolv2.Mutation }
	if err := json.Unmarshal(out, &leased); err != nil || leased.Result.Grant == nil {
		t.Fatal("missing lease fixture grant", err)
	}
	token := leased.Result.Grant.LeaseToken
	t.Setenv("BENCH_LEASE_TOKEN", token)
	out, err = localCLI(t, store, "checkpoint", "card")
	if err != nil || bytes.Contains(out, []byte(token)) {
		t.Fatal("ordinary checkpoint lost ambient authority or exported it", err)
	}
	before, err := store.Snapshot("card")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := localCLI(t, store, "checkpoint", "--token", "", "card"); err == nil || len(out) != 0 {
		t.Fatal("explicit empty token silently reused ambient capability", err)
	}
	after, err := store.Snapshot("card")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("explicit empty capability changed owned workspace", err)
	}
}

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
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

func TestWorkspaceMissingTransferCLIReviewedMixedRecoveryAndRetainedFailure(t *testing.T) {
	store, members := workspaceFixture(t)
	root, err := filepath.EvalSymlinks(filepath.Dir(store.State))
	if err != nil {
		t.Fatal(err)
	}
	store.State = filepath.Join(root, filepath.Base(store.State))
	store.Data = filepath.Join(root, filepath.Base(store.Data))
	localCreate(t, store, members)
	if out, err := localCLI(t, store, "inspect-missing-transfer", "--target", "destination", "--transfer-id", "missing-cli-1", "card"); err == nil || len(out) != 0 {
		t.Fatal("all-present source accepted missing authority", err)
	}
	sourceRows, err := store.Snapshot("card")
	if err != nil || len(sourceRows) != 1 {
		t.Fatal(err)
	}
	source := sourceRows[0]
	present := source.Workspace.Members[0]
	missing := source.Workspace.Members[1]
	if err := os.Mkdir(filepath.Join(present.Path, "ignored"), 0700); err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(present.Path, "ignored", "private-output")
	if err := os.WriteFile(privatePath, []byte("preserve source private bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	admin := cliGit(t, present.Path, "rev-parse", "--absolute-git-dir")
	index, err := os.ReadFile(filepath.Join(admin, "index"))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.AcquireWorkspaceOwnerContext(context.Background(), "card", "stopped-missing-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StopWorkspace("card", owner.Lease.Token, false); err != nil {
		t.Fatal(err)
	}
	// This unrelated unsealed intent is a fixture for preservation, not a recovered
	// command outcome. Missing-source administration must never broadly release it.
	db, err := statestore.Open(filepath.Join(store.State, "bench.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	h := history.Record{ID: strings.Repeat("7", 32), Key: "prior-history", Holder: "prior-owner", StartedAt: time.Now().UTC()}
	if err := history.Start(tx, h); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var historyBefore string
	if err := db.QueryRow("SELECT record FROM run_history WHERE id=?", h.ID).Scan(&historyBefore); err != nil {
		t.Fatal(err)
	}
	db.Close()
	receiptsPath := filepath.Join(store.State, "service.db")
	receipts, err := statestore.Open(receiptsPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receipts.Exec("CREATE TABLE receipts(owner INTEGER,id TEXT,digest TEXT,state TEXT,body BLOB,status INTEGER,PRIMARY KEY(owner,id)); INSERT INTO receipts VALUES(42,'prior-request','prior-digest','done',X'7b7d',200)"); err != nil {
		t.Fatal(err)
	}
	receipts.Close()
	receiptBytes, err := os.ReadFile(receiptsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(missing.Path); err != nil {
		t.Fatal(err)
	}
	out, err := localCLI(t, store, "inspect-missing-transfer", "--target", "destination", "--transfer-id", "missing-cli-1", "card")
	if err != nil {
		t.Fatal("inspect mixed missing source", err, string(out))
	}
	plan, err := workspacetransfer.ParseMissingPlan(out)
	if err != nil || len(plan.Members) != 2 || plan.Members[0].Present == nil || plan.Members[1].Missing == nil || plan.Recovery.Holder != owner.Lease.Holder {
		t.Fatal("missing CLI plan lost reviewed source authority", err)
	}
	if bytes.Contains(out, []byte(owner.Lease.Token)) {
		t.Fatal("missing inspection exposed old token")
	}
	path := filepath.Join(t.TempDir(), "missing-plan.json")
	if err := os.WriteFile(path, out, 0600); err != nil {
		t.Fatal(err)
	}
	beforeBegin, err := store.Snapshot("card")
	if err != nil {
		t.Fatal(err)
	}
	duplicate := append(append([]byte(nil), bytes.TrimSpace(out)[:len(bytes.TrimSpace(out))-1]...), []byte(`,"Digest":"`+plan.Digest+`"}`)...)
	duplicatePath := filepath.Join(filepath.Dir(path), "duplicate.json")
	if err := os.WriteFile(duplicatePath, duplicate, 0600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct{ path, digest, key string }{{path, strings.Repeat("0", 64), "card"}, {path, plan.Digest, "other"}, {duplicatePath, plan.Digest, "card"}} {
		if out, err := localCLI(t, store, "begin-missing-transfer", "--plan", bad.path, "--approve", bad.digest, bad.key); err == nil || len(out) != 0 {
			t.Fatal("inexact missing approval reached manager", err)
		}
		after, err := store.Snapshot("card")
		if err != nil || !reflect.DeepEqual(beforeBegin, after) {
			t.Fatal("inexact approval changed stopped owner", err)
		}
	}
	out, err = localCLI(t, store, "begin-missing-transfer", "--plan", path, "--approve", plan.Digest, "card")
	if err != nil {
		t.Fatal(err, string(out))
	}
	var begun workspacetransfer.Result
	if err := json.Unmarshal(out, &begun); err != nil || begun.Phase != "started" || begun.GrantToken != "" {
		t.Fatal("missing begin published a writer", err)
	}
	sourceRows, err = store.Snapshot("card")
	if err != nil || sourceRows[0].Lease != nil || sourceRows[0].Workspace.Transfer.Recovery == nil {
		t.Fatal("source owner not explicitly retired", err)
	}
	if _, err := store.AcquireWorkspaceOwnerContext(context.Background(), "card", "later-owner"); err == nil {
		t.Fatal("retired source reacquired")
	}
	if out, err := localCLI(t, store, "continue-missing-transfer", "--transfer-id", plan.ID, "wrong-source"); err == nil || len(out) != 0 {
		t.Fatal("wrong source continued missing approval", err)
	}
	hook := filepath.Join(plan.Repositories[0].Repository, ".git", "hooks", "pre-receive")
	body := "#!/bin/sh\nwhile read old new ref; do\n if [ \"$ref\" = 'refs/heads/superbench/checkpoints/" + plan.TargetID + "' ]; then exit 1; fi\ndone\nexit 0\n"
	if err := os.WriteFile(hook, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	out, err = localCLI(t, store, "continue-missing-transfer", "--transfer-id", plan.ID, "card")
	if err == nil || len(out) == 0 {
		t.Fatal("failed destination seal omitted retained result", err)
	}
	var retained workspacetransfer.Result
	if err := json.Unmarshal(out, &retained); err != nil || retained.GrantToken == "" || retained.GrantToken == owner.Lease.Token || retained.Phase == "completed" {
		t.Fatal("retained missing result lost correct new owner", err)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, err = localCLI(t, store, "continue-missing-transfer", "--transfer-id", plan.ID, "card")
		if err != nil {
			t.Fatal(err, string(out))
		}
		var complete workspacetransfer.Result
		if err := json.Unmarshal(out, &complete); err != nil || complete.Phase != "completed" || complete.GrantToken != "" || complete.TargetID != plan.TargetID {
			t.Fatal("missing completion/replay changed target authority", err)
		}
	}
	if got, err := os.ReadFile(privatePath); err != nil || string(got) != "preserve source private bytes" {
		t.Fatal("retained source private bytes changed", err)
	}
	if got, err := os.ReadFile(filepath.Join(admin, "index")); err != nil || !bytes.Equal(got, index) {
		t.Fatal("source index changed", err)
	}
	if _, err := os.Lstat(missing.Path); !os.IsNotExist(err) {
		t.Fatal("lost checkout recreated at source", err)
	}
	if got, err := os.ReadFile(receiptsPath); err != nil || !bytes.Equal(got, receiptBytes) {
		t.Fatal("receipt database changed", err)
	}
	db, err = statestore.Open(filepath.Join(store.State, "bench.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var historyAfter string
	if err := db.QueryRow("SELECT record FROM run_history WHERE id=?", h.ID).Scan(&historyAfter); err != nil || historyAfter != historyBefore {
		t.Fatal("missing transfer rewrote prior history", err)
	}
	targets, err := store.Snapshot("destination")
	if err != nil || len(targets) != 1 || targets[0].Lease != nil || targets[0].Workspace.Phase != "ready" {
		t.Fatal("destination did not reach warm readiness", err)
	}
	for i, m := range targets[0].Workspace.Members {
		if m.Branch != source.Workspace.Members[i].Branch || m.Base != source.Workspace.Members[i].Base || cliGit(t, m.Path, "symbolic-ref", "HEAD") != "refs/heads/"+m.Branch {
			t.Fatal("missing transfer lost original task ref/integration base")
		}
	}
}

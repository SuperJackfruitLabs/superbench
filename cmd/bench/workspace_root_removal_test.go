package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacearchive"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

func TestWorkspaceRootRemovalRejectsForeignAuthorityBeforeEffects(t *testing.T) {
	t.Setenv("BENCH_LEASE_TOKEN", strings.Repeat("e", 32))
	for _, op := range []string{"inspect-root-removal", "reconcile-root-removal"} {
		for _, flags := range [][]string{{"--socket", ""}, {"--socket", "/unused"}, {"--token", ""}, {"--token", "private"}, {"--holder", "owner"}, {"--request-id", "id"}, {"--repair-id", "id"}, {"--transfer-id", "id"}, {"--quota-bytes", "0"}, {"--target", "other"}, {"--state", ""}, {"--data", ""}, {"--plan", ""}, {"--approve", ""}, {"--disposition-id", ""}} {
			t.Run(op+strings.Join(flags, "="), func(t *testing.T) {
				root := t.TempDir()
				args := []string{op, "--state", filepath.Join(root, "state"), "--data", filepath.Join(root, "data")}
				args = append(args, flags...)
				args = append(args, "source")
				var out, stderr bytes.Buffer
				err := workspaceCLIContext(context.Background(), args, &out, &stderr)
				if err == nil || !strings.Contains(err.Error(), "flag") || out.Len() != 0 || strings.Contains(stderr.String(), strings.Repeat("e", 32)) {
					t.Fatal("foreign or empty authority reached root administration", err)
				}
				for _, name := range []string{"state", "data"} {
					if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
						t.Fatal("flag refusal created state or data", err)
					}
				}
			})
		}
	}
	for _, op := range []string{"status", "create", "plan-reap", "inspect-transfer", "inspect-initialization"} {
		var out, stderr bytes.Buffer
		if err := workspaceCLIContext(context.Background(), []string{op, "--disposition-id", "review-1", "source"}, &out, &stderr); err == nil || out.Len() != 0 {
			t.Fatal("unrelated operation accepted root disposition authority", op, err)
		}
	}
}

func TestWorkspaceRootRemovalRequiresBoundedReviewedInputs(t *testing.T) {
	root := t.TempDir()
	for name, raw := range map[string][]byte{"unknown": []byte(`{"unknown":true}`), "trailing": []byte(`{} {}`), "oversized": bytes.Repeat([]byte(" "), workspacearchive.MaxRootRemovalReview+1)} {
		if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "unknown"), filepath.Join(root, "symlink")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := [][]string{{"inspect-root-removal", "source"}, {"inspect-root-removal", "--disposition-id", "../invalid", "source"}, {"inspect-root-removal", "--disposition-id", "review-1", "../source"}, {"reconcile-root-removal", "source"}}
	for _, name := range []string{"unknown", "trailing", "oversized", "symlink", "pipe"} {
		requests = append(requests, []string{"reconcile-root-removal", "--plan", filepath.Join(root, name), "--approve", strings.Repeat("a", 64), "source"})
	}
	for _, request := range requests {
		args := []string{request[0], "--state", filepath.Join(root, "state"), "--data", filepath.Join(root, "data")}
		args = append(args, request[1:]...)
		var out, stderr bytes.Buffer
		if err := workspaceCLIContext(context.Background(), args, &out, &stderr); err == nil || out.Len() != 0 {
			t.Fatal("unreviewed root removal input reached administration", request, err)
		}
		if _, err := os.Lstat(filepath.Join(root, "state")); !os.IsNotExist(err) {
			t.Fatal("invalid input initialized state", err)
		}
	}
}

func TestWorkspaceRootRemovalCLIResolvesInterruptedEmptyRootWithoutReaping(t *testing.T) {
	store, members := workspaceFixture(t)
	root, err := filepath.EvalSymlinks(filepath.Dir(store.State))
	if err != nil {
		t.Fatal(err)
	}
	store.State, store.Data = filepath.Join(root, "state"), filepath.Join(root, "data")
	localCreate(t, store, members)
	rows, err := store.Snapshot("card")
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	source := rows[0]
	if err := os.RemoveAll(source.Workspace.Members[1].Path); err != nil {
		t.Fatal(err)
	}
	transfer := workspacetransfer.Manager{Store: store}
	plan, err := transfer.InspectMissing(context.Background(), "card", "destination", "root-cli-transfer", 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = transfer.BeginMissing(context.Background(), *plan); err != nil {
		t.Fatal(err)
	}
	if _, err = transfer.ContinueMissing(context.Background(), "card", plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ArchiveWorkspaceContext(context.Background(), "card", 0, nil); err != nil {
		t.Fatal(err)
	}
	reapPlan, err := store.PlanWorkspaceReapContext(context.Background(), "card")
	if err != nil {
		t.Fatal(err)
	}
	manager, ctx, err := store.WorkspaceArchiveAdministration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	save := manager.SaveIntent
	manager.SaveIntent = func(path string, raw []byte) error {
		if err := save(path, raw); err != nil {
			return err
		}
		if filepath.Base(path) == "aggregate-root-removal-card.json" {
			return errors.New("fixture interruption after removal intent, before unlink")
		}
		return nil
	}
	if _, err := manager.Reap(ctx, *reapPlan); err == nil {
		t.Fatal("fixture did not interrupt root removal")
	}
	before, err := store.Snapshot("card")
	if err != nil || before[0].Workspace.Phase != "reaping" {
		t.Fatal("missing durable reap intent", err)
	}
	if _, err = store.ReapWorkspaceContext(context.Background(), *reapPlan); err == nil {
		t.Fatal("ordinary replay deleted ambiguous present root")
	}
	t.Setenv("BENCH_LEASE_TOKEN", strings.Repeat("d", 32))
	raw, err := localCLI(t, store, "inspect-root-removal", "--disposition-id", "review-1", "card")
	if err != nil || bytes.Contains(raw, []byte(strings.Repeat("d", 32))) {
		t.Fatal("inspect explicit current empty path", err)
	}
	review, err := workspacearchive.ParseRootRemovalReview(raw)
	if err != nil || review.Action != "remove-current-empty-directory" {
		t.Fatal("missing explicit disposition authority", err)
	}
	path := filepath.Join(root, "root-removal-review.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = localCLI(t, store, "reconcile-root-removal", "--plan", path, "--approve", strings.Repeat("0", 64), "card"); err == nil {
		t.Fatal("incorrect approval removed root")
	}
	raw, err = localCLI(t, store, "reconcile-root-removal", "--plan", path, "--approve", review.Digest, "card")
	var result map[string]string
	if err != nil || json.Unmarshal(raw, &result) != nil || len(result) != 3 || result["phase"] != "root-absent" || result["disposition_id"] != review.ID {
		t.Fatal("unexpected root-only acknowledgement", string(raw), err)
	}
	if _, err = os.Lstat(source.Path); !os.IsNotExist(err) {
		t.Fatal("approved empty path remains", err)
	}
	after, err := store.Snapshot("card")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("root-only operation changed lifecycle/history authority", err)
	}
	if _, err = localCLI(t, store, "reconcile-root-removal", "--plan", path, "--approve", review.Digest, "card"); err != nil {
		t.Fatal("absent replay failed", err)
	}
	if _, err = store.ReapWorkspaceContext(context.Background(), *reapPlan); err != nil {
		t.Fatal("original reap did not finish after explicit resolution", err)
	}
	if err = os.Mkdir(source.Path, 0700); err != nil {
		t.Fatal(err)
	}
	if raw, err = localCLI(t, store, "reconcile-root-removal", "--plan", path, "--approve", review.Digest, "card"); err == nil || len(raw) != 0 {
		t.Fatal("old disposition replay adopted a returned directory", err)
	}
	if _, err = os.Lstat(source.Path); err != nil {
		t.Fatal("returned empty directory was removed", err)
	}
}

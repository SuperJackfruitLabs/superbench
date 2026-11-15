package workspaceapi_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceapi"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

func TestStatusReportsRealRelinquishingAndTransferredSourcesUnavailable(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repository")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		c := gitcontrol.Command(context.Background(), repo, args...)
		c.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "fixture")
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	_, err = store.InitializeWorkspaceContext(context.Background(), "source-create", workspace.Spec{Key: "source", CheckpointRepository: "api", Repositories: []workspace.Repository{{Name: "api", Repository: repo}}})
	if err != nil {
		t.Fatal(err)
	}
	status := func(key string) *protocolv2.WorkspaceInventory {
		t.Helper()
		request := protocolv2.Request{Version: protocolv2.Version, Operation: "status", Key: key}
		response, httpStatus, err := workspaceapi.Execute(context.Background(), store, request, 42)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "Recovery") || strings.Contains(string(raw), "run-") || strings.Contains(string(raw), "lease_token") {
			t.Fatal("status leaked private transfer authority")
		}
		parsed, err := protocolv2.ParseResponse(raw, httpStatus, request)
		if err != nil {
			t.Fatal(err)
		}
		rows := parsed.Result.(*protocolv2.Status).Benches
		if len(rows) != 1 {
			t.Fatal("specific status omitted registered source")
		}
		return rows[0].(*protocolv2.WorkspaceInventory)
	}
	before := status("source")
	if !before.ReadyForAcquisition || before.TransferState != "" {
		t.Fatal("ordinary source unavailable before handoff")
	}
	manager := workspacetransfer.Manager{Store: store}
	plan, err := manager.Inspect(context.Background(), "source", "destination", "transfer-status", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Begin(context.Background(), *plan); err != nil {
		t.Fatal(err)
	}
	retiring := status("source")
	if retiring.TransferState != "relinquishing" || retiring.ReadyForAcquisition || retiring.Phase != "ready" || retiring.Lease != nil {
		t.Fatal("relinquishing source advertised a writer or lost initialization phase")
	}
	if _, err = manager.Continue(context.Background(), "source", plan.ID); err != nil {
		t.Fatal(err)
	}
	retired := status("source")
	if retired.TransferState != "transferred" || retired.ReadyForAcquisition || retired.Phase != "ready" {
		t.Fatal("transferred source advertised acquisition")
	}
	target := status("destination")
	if target.TransferState != "" || !target.ReadyForAcquisition {
		t.Fatal("incoming fresh workspace inherited source unavailability")
	}
	if _, err = store.AcquireWorkspaceOwnerContext(context.Background(), "source", "later"); err == nil {
		t.Fatal("status unavailability disagreed with actual writer gate")
	}
}

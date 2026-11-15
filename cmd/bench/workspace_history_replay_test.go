package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

// Seed an already reviewed observation to exercise successful CLI idempotent
// replay without claiming that this native test fenced any actual execution.
func TestWorkspaceHistoryCLIReviewedReplayDoesNotExportOwnerOrInventOutcome(t *testing.T) {
	store, members := workspaceFixture(t)
	created := localCreate(t, store, members)
	raw, err := localCLI(t, store, "lease", "--holder", "current-owner", "--request-id", "current-history-owner", "card")
	if err != nil {
		t.Fatal(err, string(raw))
	}
	var leased struct{ Result protocolv2.Mutation }
	if err := json.Unmarshal(raw, &leased); err != nil {
		t.Fatal(err)
	}
	token := leased.Result.Grant.LeaseToken
	before, err := store.Snapshot("card")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("c", 32)
	at := time.Now().UTC()
	plan := workspaceprep.HistoryPlan{Version: "superbench.workspace-history.v1", Root: store.State, Key: "card", BenchID: created.Workspace.BenchID,
		BenchDigest: strings.Repeat("a", 64), ObservedAt: at,
		Execution: execution.Execution{ID: id, Root: filepath.Join(store.State, "unused-scope"), Phase: "stopped", HostID: "fixture-host", BootID: "fixture-boot", Log: filepath.Join(store.State, "run-"+id+".log"), LogLimitBytes: 4096}}
	var expected history.Record
	err = statestore.Within(store.State, nil, func(session *statestore.Session) error {
		return session.Update(func(tx *sql.Tx) error {
			original := history.Record{ID: id, Origin: "supervised", Key: "card", Holder: "former-owner", StartedAt: at.Add(-time.Hour)}
			if err := history.Start(tx, original); err != nil {
				return err
			}
			var err error
			plan.PriorDigest, err = history.Digest(original)
			if err != nil {
				return err
			}
			body, err := json.Marshal(plan)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(body)
			plan.Digest = hex.EncodeToString(digest[:])
			expected, err = history.ObserveInterrupted(tx, id, plan.PriorDigest, history.InterruptedObservation{ApprovalDigest: plan.Digest, PriorDigest: plan.PriorDigest, LogState: "absent", ObservedAt: at}, nil, 0)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	planBytes, err := plan.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "history-plan.json")
	if err := os.WriteFile(file, planBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := localCLI(t, store, "reconcile-history", "--plan", file, "--approve", strings.Repeat("b", 64), "card"); err == nil || len(out) != 0 {
		t.Fatal("wrong approval accepted", err)
	}
	for i := 0; i < 2; i++ {
		out, err := localCLI(t, store, "reconcile-history", "--plan", file, "--approve", plan.Digest, "card")
		if err != nil {
			t.Fatal("reviewed replay failed", err)
		}
		if len(out) > workspaceprep.MaxHistoryPlan || strings.Contains(string(out), token) || strings.Contains(string(out), "Continuation") {
			t.Fatal("history output exceeded bound or exported owner")
		}
		var record history.Record
		if err := json.Unmarshal(out, &record); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(record, expected) || record.ExitCode != nil || record.File != nil || record.Truncated || record.Interruption.LogState != "absent" {
			t.Fatal("CLI changed unknown historical observation")
		}
	}
	after, err := store.Snapshot("card")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("reviewed replay changed current owner", err)
	}
	if err := store.CheckWorkspaceOwner("card", token, "current-owner"); err != nil {
		t.Fatal("reviewed replay revoked/replaced current owner", err)
	}
	var out, stderr strings.Builder
	if err := workspaceCLIContext(context.Background(), []string{"inspect-history", "--state", store.State, "--data", store.Data, "card"}, &out, &stderr); err == nil || out.Len() != 0 {
		t.Fatal("inspection fabricated an interrupted current execution", err)
	}
}

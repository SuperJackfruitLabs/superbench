package workspaceflow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

// The wrapper supplies an actual reviewed schema18 binary. No host policy,
// mount or quota allocation is created by this compatibility assertion.
func TestManagedWorkspaceHistorySchema18Refusal(t *testing.T) {
	trial := os.Getenv("SUPERBENCH_WORKSPACE_MANAGED_TRIAL")
	old := os.Getenv("SUPERBENCH_WORKSPACE_OLD_SCHEMA18_CLI")
	if trial == "" || old == "" {
		t.Skip("opt-in disposable managed fixture and reviewed schema18 CLI required")
	}
	if runtime.GOOS != "linux" || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || !strings.HasPrefix(trial, "/var/tmp/superbench-workspace.") || filepath.Clean(trial) != trial || !filepath.IsAbs(old) {
		t.Fatal("invalid disposable managed compatibility fixture")
	}
	state := filepath.Join(trial, "history-compat-state")
	data := filepath.Join(trial, "data")
	store := bench.Store{State: state, Data: data}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	db, err := statestore.Open(filepath.Join(state, "bench.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	seedInterruptedCompatibilityHistory(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := compatibilityStateFiles(t, state)
	for _, operation := range []string{"status", "stop"} {
		args := []string{operation, "--state", state, "--data", data}
		if operation == "stop" {
			args = append(args, "--admin", "interrupted")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, old, args...)
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil || strings.TrimSpace(string(out)) != "bench: unsupported state schema 20 (expected 18)" {
			t.Fatalf("reviewed schema18 %s did not produce only the expected refusal: %v %q", operation, err, out)
		}
		if !reflect.DeepEqual(before, compatibilityStateFiles(t, state)) {
			t.Fatal("old binary changed schema20 history, receipts or files")
		}
	}
	t.Log("PASS: actual schema18 CLI status and mutation refuse schema19 before changing interrupted history, receipt evidence or state files")
}

func seedInterruptedCompatibilityHistory(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	r := history.Record{ID: strings.Repeat("c", 32), Origin: "supervised", Key: "interrupted", Holder: "fixture", StartedAt: time.Now().UTC().Add(-time.Minute)}
	if err := history.Start(tx, r); err != nil {
		t.Fatal(err)
	}
	prior, err := history.Digest(r)
	if err != nil {
		t.Fatal(err)
	}
	observation := history.InterruptedObservation{ApprovalDigest: strings.Repeat("d", 64), PriorDigest: prior, LogState: "absent", ObservedAt: time.Now().UTC()}
	if _, err := history.ObserveInterrupted(tx, r.ID, prior, observation, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("CREATE TABLE compatibility_receipts (id TEXT PRIMARY KEY,record TEXT NOT NULL); INSERT INTO compatibility_receipts VALUES('interrupted-request','retained-replay-evidence')"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func compatibilityStateFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			t.Fatal("unexpected compatibility state artifact", entry.Name(), err)
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		out[entry.Name()] = info.Mode().String() + ":" + hex.EncodeToString(sum[:])
	}
	return out
}

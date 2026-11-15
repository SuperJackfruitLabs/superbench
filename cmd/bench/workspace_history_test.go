package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

func TestWorkspaceHistoryCLIRejectsRemoteAndUnrelatedFlagsBeforeEffects(t *testing.T) {
	for _, op := range []string{"inspect-history", "reconcile-history"} {
		for _, flag := range [][]string{{"--socket", ""}, {"--socket", "/unused/socket"}, {"--repository-name", "api"}, {"--holder", "worker"}, {"--token", "synthetic"}, {"--request-id", "request"}, {"--initialize=false"}, {"--archive-grace", "0"}} {
			t.Run(op+flag[0], func(t *testing.T) {
				root := t.TempDir()
				state, data := filepath.Join(root, "state"), filepath.Join(root, "data")
				args := []string{op, "--state", state, "--data", data}
				args = append(args, flag...)
				args = append(args, "card")
				var stdout, stderr bytes.Buffer
				if err := workspaceCLIContext(context.Background(), args, &stdout, &stderr); err == nil || stdout.Len() != 0 {
					t.Fatal("local history operation accepted unrelated authority", err)
				}
				for _, path := range []string{state, data} {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatal("flag refusal initialized local state", err)
					}
				}
			})
		}
	}
}

func TestWorkspaceHistoryReviewedInputRefusalsPreserveOwnedWorkspace(t *testing.T) {
	store, members := workspaceFixture(t)
	created := localCreate(t, store, members)
	if raw, err := localCLI(t, store, "lease", "--holder", "history-owner", "--request-id", "history-lease", "card"); err != nil {
		t.Fatal(err, string(raw))
	}
	before, err := store.Snapshot("card")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	files := map[string][]byte{
		"duplicate.json": []byte(`{"Digest":"first","Digest":"second"}`),
		"unknown.json":   []byte(`{"Unrecognized":true}`),
		"trailing.json":  []byte(`{} {}`),
		"oversized.json": []byte(strings.Repeat(" ", workspaceprep.MaxHistoryPlan+1)),
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "duplicate.json"), filepath.Join(root, "symlink.json")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"duplicate.json", "unknown.json", "trailing.json", "oversized.json", "symlink.json", "pipe"} {
		t.Run(name, func(t *testing.T) {
			out, err := localCLI(t, store, "reconcile-history", "--plan", filepath.Join(root, name), "--approve", strings.Repeat("a", 64), "card")
			if err == nil || len(out) != 0 {
				t.Fatal("invalid reviewed history input reached reconciliation", err)
			}
			after, err := store.Snapshot("card")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("review refusal changed retained owner", err)
			}
			if raw, err := os.ReadFile(filepath.Join(created.RootPath, "api", "source.txt")); err != nil || string(raw) != "api" {
				t.Fatal("review refusal changed source files", err)
			}
		})
	}
	if out, err := localCLI(t, store, "reconcile-history", "card"); err == nil || len(out) != 0 {
		t.Fatal("missing concrete approval reached backend", err)
	}
	if out, err := localCLI(t, store, "inspect-history", "--plan", filepath.Join(root, "duplicate.json"), "card"); err == nil || len(out) != 0 {
		t.Fatal("inspection accepted an apply plan", err)
	}
}

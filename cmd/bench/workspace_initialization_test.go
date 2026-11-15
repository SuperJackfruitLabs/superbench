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

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
)

func TestWorkspaceInitializationFlagsRefuseBeforeEffects(t *testing.T) {
	operations := []string{"inspect-initialization", "claim-initialization", "continue-initialization"}
	for _, op := range operations {
		flags := [][]string{{"--socket", ""}, {"--socket", "/unused/socket"}, {"--state", ""}, {"--data", ""}, {"--cgroup-root", ""}, {"--repository-name", "api"}, {"--holder", "worker"}, {"--request-id", "request"}, {"--initialize=false"}, {"--archive-grace", "0"}, {"--repair-id", ""}, {"--rerun-once", ""}, {"--plan", ""}, {"--approve", ""}, {"--token", ""}}
		if op != "inspect-initialization" {
			flags = append(flags, []string{"--rerun-once", "api"})
		}
		if op != "claim-initialization" {
			flags = append(flags, []string{"--plan", "/unused/plan"}, []string{"--approve", "digest"})
		}
		if op != "continue-initialization" {
			flags = append(flags, []string{"--token", "synthetic"})
		}
		for _, forbidden := range flags {
			t.Run(op+strings.Join(forbidden, "="), func(t *testing.T) {
				root := t.TempDir()
				state, data := filepath.Join(root, "state"), filepath.Join(root, "data")
				args := []string{op, "--state", state, "--data", data}
				args = append(args, forbidden...)
				args = append(args, "card")
				var stdout, stderr bytes.Buffer
				if err := workspaceCLIContext(context.Background(), args, &stdout, &stderr); err == nil || stdout.Len() != 0 {
					t.Fatal("invalid repair authority reached backend", err)
				}
				for _, path := range []string{state, data} {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatal("flag refusal initialized local state", err)
					}
				}
			})
		}
	}
	for _, op := range []string{"status", "setup", "create", "stop_admin"} {
		for _, flag := range []string{"--repair-id", "--rerun-once"} {
			root := t.TempDir()
			args := []string{op, "--socket", filepath.Join(root, "absent.socket"), "--state", filepath.Join(root, "state"), flag, "", "card"}
			var stdout, stderr bytes.Buffer
			if err := workspaceCLIContext(context.Background(), args, &stdout, &stderr); err == nil || stdout.Len() != 0 {
				t.Fatal("repair flag accepted by ordinary remote operation", op, flag, err)
			}
			if _, err := os.Lstat(filepath.Join(root, "state")); !os.IsNotExist(err) {
				t.Fatal("remote flag refusal initialized state", err)
			}
		}
	}
}

func TestWorkspaceInitializationRequiresExplicitContinuationCapability(t *testing.T) {
	ambient := strings.Repeat("a", 32)
	t.Setenv("BENCH_LEASE_TOKEN", ambient)
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := workspaceCLIContext(context.Background(), []string{"continue-initialization", "--state", filepath.Join(root, "state"), "--repair-id", "repair-1", "card"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "explicit --token") || stdout.Len() != 0 {
		t.Fatal("ambient token became repair authority", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatal("missing explicit capability initialized state", err)
	}
	for _, op := range []string{"inspect-initialization", "claim-initialization", "continue-initialization", "run", "setup", "stop", "status"} {
		stdout.Reset()
		stderr.Reset()
		if err := workspaceCLIContext(context.Background(), []string{op, "--unknown-flag"}, &stdout, &stderr); err == nil || stdout.Len() != 0 || strings.Contains(stderr.String(), ambient) {
			t.Fatal("repair flag parsing exported ambient capability in usage", op, err)
		}
	}
}

func TestWorkspaceInitializationMissingApprovalAndCancellationHaveNoEffects(t *testing.T) {
	for _, args := range [][]string{
		{"inspect-initialization"},
		{"claim-initialization", "card"},
		{"claim-initialization", "--repair-id", "repair-1", "card"},
		{"continue-initialization", "--token", strings.Repeat("a", 32), "card"},
		{"continue-initialization", "--repair-id", "repair-1", "--token", strings.Repeat("a", 32)},
	} {
		root := t.TempDir()
		command := []string{args[0], "--state", filepath.Join(root, "state"), "--data", filepath.Join(root, "data")}
		command = append(command, args[1:]...)
		var stdout, stderr bytes.Buffer
		if err := workspaceCLIContext(context.Background(), command, &stdout, &stderr); err == nil || stdout.Len() != 0 {
			t.Fatal("incomplete repair request reached backend", args, err)
		}
		for _, path := range []string{"state", "data"} {
			if _, err := os.Lstat(filepath.Join(root, path)); !os.IsNotExist(err) {
				t.Fatal("incomplete repair request initialized state", err)
			}
		}
	}
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	err := workspaceCLIContext(ctx, []string{"inspect-initialization", "--state", filepath.Join(root, "state"), "card"}, &stdout, &stderr)
	if err != context.Canceled || stdout.Len() != 0 {
		t.Fatal("canceled inspection reached backend", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatal("canceled inspection initialized state", err)
	}
}

func TestWorkspaceInitializationRejectsUnsafePlansWithoutWorkspaceEffects(t *testing.T) {
	store, members := workspaceFixture(t)
	created := localCreate(t, store, members)
	before, err := store.Snapshot("card")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	files := map[string][]byte{
		"duplicate.json": []byte(`{"Digest":"first","Digest":"second"}`),
		"unknown.json":   []byte(`{"Unrecognized":true}`),
		"trailing.json":  []byte(`{} {}`),
		"oversized.json": []byte(strings.Repeat(" ", workspacerepair.MaxPlan+1)),
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
			out, err := localCLI(t, store, "claim-initialization", "--plan", filepath.Join(root, name), "--approve", strings.Repeat("a", 64), "--repair-id", "repair-1", "card")
			if err == nil || len(out) != 0 {
				t.Fatal("unsafe plan reached repair claim", err)
			}
			after, err := store.Snapshot("card")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("invalid approval changed workspace ownership", err)
			}
			if raw, err := os.ReadFile(filepath.Join(created.RootPath, "api", "source.txt")); err != nil || string(raw) != "api" {
				t.Fatal("invalid approval changed source files", err)
			}
		})
	}
}

func TestWorkspaceInitializationRerunOnceSelection(t *testing.T) {
	for _, value := range []string{"api,api", "api,", ",api", "api, web", "../api", "API", strings.Repeat("a", 65), strings.Repeat("api,", 32) + "web"} {
		root := t.TempDir()
		var stdout, stderr bytes.Buffer
		err := workspaceCLIContext(context.Background(), []string{"inspect-initialization", "--state", filepath.Join(root, "state"), "--rerun-once", value, "card"}, &stdout, &stderr)
		if err == nil || stdout.Len() != 0 {
			t.Fatal("invalid uncertain-once selection reached inspect", value, err)
		}
		if _, err := os.Lstat(filepath.Join(root, "state")); !os.IsNotExist(err) {
			t.Fatal("invalid member selection initialized state", err)
		}
	}
}

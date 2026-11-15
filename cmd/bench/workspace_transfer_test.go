package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

func TestWorkspaceTransferFlagsRefuseBeforeEffects(t *testing.T) {
	ambient := strings.Repeat("e", 32)
	t.Setenv("BENCH_LEASE_TOKEN", ambient)
	for _, op := range []string{"inspect-transfer", "begin-transfer", "continue-transfer"} {
		forbidden := [][]string{{"--socket", ""}, {"--socket", "/unused/socket"}, {"--token", ""}, {"--token", "synthetic"}, {"--holder", "worker"}, {"--request-id", "request"}, {"--repository-name", "api"}, {"--repair-id", "repair"}, {"--rerun-once", "api"}, {"--initialize=false"}, {"--archive-grace", "0"}, {"--state", ""}, {"--target", ""}, {"--transfer-id", ""}, {"--plan", ""}, {"--approve", ""}, {"--unknown-flag"}}
		if op != "inspect-transfer" {
			forbidden = append(forbidden, []string{"--target", "destination"}, []string{"--quota-bytes", "0"})
		}
		if op != "begin-transfer" {
			forbidden = append(forbidden, []string{"--plan", "/unused/plan"}, []string{"--approve", "digest"})
		} else {
			forbidden = append(forbidden, []string{"--transfer-id", "transfer-1"})
		}
		for _, flag := range forbidden {
			t.Run(op+strings.Join(flag, "="), func(t *testing.T) {
				root := t.TempDir()
				args := []string{op, "--state", filepath.Join(root, "state"), "--data", filepath.Join(root, "data")}
				args = append(args, flag...)
				args = append(args, "source")
				var stdout, stderr bytes.Buffer
				if err := workspaceCLIContext(context.Background(), args, &stdout, &stderr); err == nil || stdout.Len() != 0 || strings.Contains(stderr.String(), ambient) {
					t.Fatal("invalid transfer authority accepted or ambient capability disclosed", err)
				}
				for _, path := range []string{"state", "data"} {
					if _, err := os.Lstat(filepath.Join(root, path)); !os.IsNotExist(err) {
						t.Fatal("flag refusal initialized local state", err)
					}
				}
			})
		}
	}
	for _, op := range []string{"status", "setup", "create", "inspect-initialization", "plan-reap"} {
		for _, flag := range []string{"--target", "--transfer-id"} {
			var stdout, stderr bytes.Buffer
			if err := workspaceCLIContext(context.Background(), []string{op, flag, "", "source"}, &stdout, &stderr); err == nil || stdout.Len() != 0 {
				t.Fatal("unrelated workspace operation accepted transfer flags", op, flag, err)
			}
		}
	}
}

func TestWorkspaceTransferRequiresConcreteInputsBeforeEffects(t *testing.T) {
	for _, request := range [][]string{
		{"inspect-transfer", "--target", "destination", "source"},
		{"inspect-transfer", "--transfer-id", "transfer-1", "source"},
		{"inspect-transfer", "--target", "destination", "--transfer-id", "transfer-1"},
		{"begin-transfer", "source"},
		{"continue-transfer", "source"},
		{"continue-transfer", "--transfer-id", "transfer-1"},
	} {
		root := t.TempDir()
		args := []string{request[0], "--state", filepath.Join(root, "state"), "--data", filepath.Join(root, "data")}
		args = append(args, request[1:]...)
		var stdout, stderr bytes.Buffer
		if err := workspaceCLIContext(context.Background(), args, &stdout, &stderr); err == nil || stdout.Len() != 0 {
			t.Fatal("incomplete transfer request reached manager", request, err)
		}
		for _, path := range []string{"state", "data"} {
			if _, err := os.Lstat(filepath.Join(root, path)); !os.IsNotExist(err) {
				t.Fatal("incomplete transfer request initialized state", err)
			}
		}
	}
	root := t.TempDir()
	bad := map[string][]byte{"unknown.json": []byte(`{"unknown":true}`), "trailing.json": []byte(`{} {}`), "oversized.json": []byte(strings.Repeat(" ", workspacetransfer.MaxPlan+1))}
	for name, raw := range bad {
		if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "unknown.json"), filepath.Join(root, "symlink")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unknown.json", "trailing.json", "oversized.json", "symlink", "pipe"} {
		var stdout, stderr bytes.Buffer
		err := workspaceCLIContext(context.Background(), []string{"begin-transfer", "--state", filepath.Join(root, "state"), "--data", filepath.Join(root, "data"), "--plan", filepath.Join(root, name), "--approve", strings.Repeat("a", 64), "source"}, &stdout, &stderr)
		if err == nil || stdout.Len() != 0 {
			t.Fatal("unsafe transfer plan accepted", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatal("unsafe plan initialized local state", err)
	}
}

func TestWorkspaceTransferResultExpansionBoundBeforeStdout(t *testing.T) {
	result := &workspacetransfer.Result{Version: workspacetransfer.Version, RootPath: strings.Repeat("<", workspacetransfer.MaxResult/6)}
	var stdout bytes.Buffer
	if err := writeWorkspaceTransferResult(&stdout, result); err == nil || stdout.Len() != 0 {
		t.Fatal("expanded transfer JSON exceeded bound or partially wrote", err)
	}
}

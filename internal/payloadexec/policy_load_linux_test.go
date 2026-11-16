//go:build linux

package payloadexec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPolicyFIFORefusesWithoutWaitingForWriter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "policy")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestPolicyFIFOReadHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "SUPERBENCH_PAYLOAD_FIFO_PARENT="+root)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil || err != nil || !bytes.Contains(out, []byte("--- PASS: TestPolicyFIFOReadHelper")) {
		t.Fatalf("FIFO refusal blocked or failed: %v %v %s", ctx.Err(), err, out)
	}
}

func TestPolicyFIFOReadHelper(t *testing.T) {
	root := os.Getenv("SUPERBENCH_PAYLOAD_FIFO_PARENT")
	if root == "" {
		return
	}
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	file, _, err := openPolicyFile(parent, "policy")
	if file != nil || !errors.Is(err, ErrPolicy) {
		t.Fatalf("expected non-regular policy refusal, got %v %v", file, err)
	}
}

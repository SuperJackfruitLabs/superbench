//go:build linux

package payloadexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStdioRejectsPrivateFileAndWrongPipeOwner(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"valid", "private-file", "wrong-owner"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestStdioVerifyHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "SUPERBENCH_PAYLOAD_STDIO_FIXTURE="+kind)
			cmd.Stdin = strings.NewReader("fixture frame input")
			if kind == "private-file" {
				file, err := os.CreateTemp(t.TempDir(), "daemon-state-")
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				cmd.Stdin = file
			}
			out, err := cmd.CombinedOutput()
			if err != nil || ctx.Err() != nil || !bytes.Contains(out, []byte("--- PASS: TestStdioVerifyHelper")) {
				t.Fatalf("stdio guard fixture: %v %v %s", err, ctx.Err(), out)
			}
		})
	}
}

func TestStdioVerifyHelper(t *testing.T) {
	kind := os.Getenv("SUPERBENCH_PAYLOAD_STDIO_FIXTURE")
	if kind == "" {
		return
	}
	p := Policy{DaemonUID: uint32(os.Getuid())}
	if kind == "wrong-owner" {
		p.DaemonUID++
	}
	err := verifyStdio(p)
	if kind == "valid" {
		if err != nil {
			t.Fatalf("valid capture pipes refused: %v", err)
		}
	} else if !errors.Is(err, ErrBoundary) {
		t.Fatalf("unsafe stdio should refuse exact boundary: %v", err)
	}
}

func TestExecDoesNotInheritExtraFileOrCaptureReader(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := os.CreateTemp(t.TempDir(), "private-control-")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Close()
	if _, err := secret.WriteString("retained daemon-private fixture"); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestDescriptorExecHelper$", "-test.v")
	cmd.ExtraFiles = []*os.File{secret, reader}
	cmd.Env = append(os.Environ(), "SUPERBENCH_PAYLOAD_DESCRIPTOR_STAGE=mark")
	out, err := cmd.CombinedOutput()
	if err != nil || ctx.Err() != nil || !bytes.Contains(out, []byte("--- PASS: TestDescriptorExecHelper")) {
		t.Fatalf("inherited capability check failed: %v %v %s", err, ctx.Err(), out)
	}
	raw, err := os.ReadFile(secret.Name())
	if err != nil || string(raw) != "retained daemon-private fixture" {
		t.Fatalf("descriptor closure changed private fixture: %q %v", raw, err)
	}
}

func TestDescriptorExecHelper(t *testing.T) {
	stage := os.Getenv("SUPERBENCH_PAYLOAD_DESCRIPTOR_STAGE")
	if stage == "" {
		return
	}
	var private []Node
	if stage == "mark" {
		for _, fd := range []int{3, 4} {
			var st syscall.Stat_t
			if err := syscall.Fstat(fd, &st); err != nil {
				t.Fatal(err)
			}
			private = append(private, Node{uint64(st.Dev), st.Ino})
		}
		raw, _ := json.Marshal(private)
		if err := descriptorsCloseOnExec(); err != nil {
			t.Fatal(err)
		}
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		var environment []string
		for _, item := range os.Environ() {
			if !strings.HasPrefix(item, "SUPERBENCH_PAYLOAD_DESCRIPTOR_") {
				environment = append(environment, item)
			}
		}
		environment = append(environment, "SUPERBENCH_PAYLOAD_DESCRIPTOR_STAGE=check", "SUPERBENCH_PAYLOAD_DESCRIPTOR_NODES="+string(raw))
		if err := syscall.Exec(executable, []string{executable, "-test.run=^TestDescriptorExecHelper$", "-test.v"}, environment); err != nil {
			t.Fatal(err)
		}
		t.Fatal("exec unexpectedly returned")
	}
	if stage != "check" || json.Unmarshal([]byte(os.Getenv("SUPERBENCH_PAYLOAD_DESCRIPTOR_NODES")), &private) != nil || len(private) != 2 {
		t.Fatal("invalid descriptor fixture")
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err == syscall.EBADF {
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		for _, want := range private {
			if (Node{uint64(st.Dev), st.Ino}) == want {
				t.Fatalf("private descriptor survived exec on fd%d", fd)
			}
		}
	}
}

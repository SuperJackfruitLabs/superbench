//go:build linux

package payloadexec

import (
	"encoding/binary"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitpolicy"
	"io"
	"os"
	"syscall"
	"testing"
	"time"
)

func frameBytes(t *testing.T, r Request) []byte {
	t.Helper()
	raw, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 4, 4+len(raw))
	binary.BigEndian.PutUint32(frame, uint32(len(raw)))
	return append(frame, raw...)
}

func framePipe(t *testing.T, raw []byte) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	go func() { _, _ = w.Write(raw); _ = w.Close() }()
	return r
}

func TestGitFramePreservesExactSubsequentStdin(t *testing.T) {
	r := launchRequest()
	r.Kind = "git"
	r.Arguments = gitpolicy.Arguments("index-pack", "--stdin", "--strict")
	pack := []byte{'P', 'A', 'C', 'K', 0, '\n', 255, 0}
	input := framePipe(t, append(frameBytes(t, r), pack...))
	got, err := readFrame(input, time.Second)
	if err != nil || got.Kind != "git" {
		t.Fatalf("Git frame: %#v %v", got, err)
	}
	rest, err := io.ReadAll(input)
	if err != nil || string(rest) != string(pack) {
		t.Fatalf("request reader consumed command stdin: %x %v", rest, err)
	}
}

func TestPayloadFrameRequiresEOFAndRejectsMalformedOrOversizeInput(t *testing.T) {
	valid := frameBytes(t, launchRequest())
	if _, err := readFrame(framePipe(t, valid), time.Second); err != nil {
		t.Fatal(err)
	}
	oversize := make([]byte, 4)
	binary.BigEndian.PutUint32(oversize, MaxRequest+1)
	for _, raw := range [][]byte{nil, {0, 0}, {0, 0, 0, 0}, oversize, valid[:len(valid)-1], append(append([]byte(nil), valid...), 'x')} {
		if _, err := readFrame(framePipe(t, raw), time.Second); !errors.Is(err, ErrFrame) {
			t.Fatalf("malformed frame accepted: %v", err)
		}
	}
}

func TestPartialFrameHasBoundedWaitAndRestoresInputFlags(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd := r.Fd()
	before, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	if _, err := w.Write([]byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := readFrame(r, 100*time.Millisecond); !errors.Is(err, ErrFrame) || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("partial frame did not time out: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("partial input held helper beyond its deadline")
	}
	after, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
	if errno != 0 || before != after {
		t.Fatalf("frame timeout changed stdin flags: %x -> %x %v", before, after, errno)
	}
}

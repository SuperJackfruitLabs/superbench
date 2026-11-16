//go:build linux

package payloadexec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func inputFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range entries {
		name, _ := os.Readlink("/proc/self/fd/" + e.Name())
		if strings.Contains(name, "memfd:superbench-payload-input") {
			count++
		}
	}
	return count
}
func TestFiniteInputIsImmutableReadOnlyAnonymousAndClosed(t *testing.T) {
	before := inputFDs(t)
	r := launchRequest()
	r.Input = bytes.Repeat([]byte{0, 255, 1}, 30000)
	f, err := openPayloadInput(r)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Sys().(*syscall.Stat_t).Nlink != 0 {
		t.Fatal("input is not private anonymous storage")
	}
	if got := inputFDs(t); got != before+1 {
		t.Fatalf("writable input handle retained: %d before %d", got, before)
	}
	fd := f.Fd()
	if _, err := syscall.Write(int(fd), []byte{1}); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("stdin is writable: %v", err)
	}
	// Even reopening this owned descriptor writable cannot change sealed bytes.
	w, err := os.OpenFile("/proc/self/fd/"+strconv.Itoa(int(fd)), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Write([]byte{1})
	closeErr := w.Close()
	if !errors.Is(err, syscall.EPERM) || closeErr != nil {
		t.Fatalf("input seals ineffective: %v %v", err, closeErr)
	}
	got, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(got, r.Input) {
		t.Fatalf("opaque bytes/EOF: %v", err)
	}
	if err := checkPayloadInput(f); !errors.Is(err, ErrFrame) {
		t.Fatalf("consumed input accepted: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := inputFDs(t); got != before {
		t.Fatalf("input handle leak: %d before %d", got, before)
	}
	var closed syscall.Stat_t
	if err := syscall.Fstat(int(fd), &closed); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("closed descriptor usable: %v", err)
	}
}
func TestFiniteInputDefaultAndRefusalsAllocateNothing(t *testing.T) {
	before := inputFDs(t)
	f, err := openPayloadInput(launchRequest())
	if err != nil || f != nil {
		t.Fatalf("default EOF changed: %v", err)
	}
	r := launchRequest()
	r.Input = make([]byte, MaxPayloadInput+1)
	if f, err := openPayloadInput(r); f != nil || !errors.Is(err, ErrRequest) {
		t.Fatalf("oversized input allocated: %v", err)
	}
	if err := bindPayloadInput(nil); !errors.Is(err, ErrFrame) {
		t.Fatalf("nil input: %v", err)
	}
	pipe, other, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	defer other.Close()
	if err := bindPayloadInput(pipe); !errors.Is(err, ErrFrame) {
		t.Fatalf("unsealed pipe accepted: %v", err)
	}
	if got := inputFDs(t); got != before {
		t.Fatalf("refusal leaked input: %d before %d", got, before)
	}
}
func TestFiniteInputExecSurvivesDescriptorClosureAndHasExactEOF(t *testing.T) {
	if os.Getenv("SB_FINITE_INPUT_CHILD") == "1" {
		f := os.NewFile(3, "sealed test input")
		if err := bindPayloadInput(f); err != nil {
			t.Fatal(err)
		}
		f.Close()
		got, err := io.ReadAll(os.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stdout.Write(got); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	r := launchRequest()
	r.Input = bytes.Repeat([]byte{0, 255, 'x'}, 30000)
	f, err := openPayloadInput(r)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFiniteInputExecSurvivesDescriptorClosureAndHasExactEOF$")
	cmd.Env = append(os.Environ(), "SB_FINITE_INPUT_CHILD=1")
	cmd.ExtraFiles = []*os.File{f}
	cmd.WaitDelay = time.Second
	got, err := cmd.Output()
	if err != nil || !bytes.Equal(got, r.Input) {
		t.Fatalf("exec input/EOF: bytes=%d err=%v", len(got), err)
	}
}
func TestFinitePayloadFrameStillRequiresExactEnvelopeEOF(t *testing.T) {
	r := launchRequest()
	r.Input = []byte{0, 255, '\n'}
	raw := frameBytes(t, r)
	got, err := readFrame(framePipe(t, raw), time.Second)
	if err != nil || !bytes.Equal(got.Input, r.Input) {
		t.Fatalf("finite frame: %v", err)
	}
	if _, err := readFrame(framePipe(t, append(raw, 1)), time.Second); !errors.Is(err, ErrFrame) {
		t.Fatalf("extra streamed byte accepted: %v", err)
	}
}

func TestFiniteInputReopenFailureClosesAnonymousStorage(t *testing.T) {
	if os.Getenv("SB_FINITE_INPUT_FAILURE_CHILD") == "1" {
		var limit syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			t.Fatal(err)
		}
		limit.Cur = 64
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			t.Fatal(err)
		}
		var files []*os.File
		for {
			f, err := os.Open("/dev/null")
			if errors.Is(err, syscall.EMFILE) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		if len(files) == 0 {
			t.Fatal("no slot for precise reopen failure")
		}
		if err := files[len(files)-1].Close(); err != nil {
			t.Fatal(err)
		}
		files = files[:len(files)-1]
		r := launchRequest()
		r.Input = []byte{1}
		f, err := openPayloadInput(r)
		for _, held := range files {
			held.Close()
		}
		if f != nil || !errors.Is(err, ErrFrame) || !errors.Is(err, syscall.EMFILE) {
			t.Fatalf("expected readonly reopen EMFILE: %v", err)
		}
		if got := inputFDs(t); got != 0 {
			t.Fatalf("reopen failure retained %d anonymous descriptors", got)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFiniteInputReopenFailureClosesAnonymousStorage$")
	cmd.Env = append(os.Environ(), "SB_FINITE_INPUT_FAILURE_CHILD=1")
	cmd.WaitDelay = time.Second
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("owned descriptor failure test: %v %s", err, output)
	}
}

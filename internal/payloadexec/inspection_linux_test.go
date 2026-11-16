//go:build linux

package payloadexec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"io"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func inspectionFrame(t *testing.T) []byte {
	t.Helper()
	raw, err := inspectionRequest().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	framed := make([]byte, 4)
	binary.BigEndian.PutUint32(framed, uint32(len(raw)))
	return append(framed, raw...)
}
func TestInspectionFrameRequiresEOFAndNeverBecomesExecution(t *testing.T) {
	valid := inspectionFrame(t)
	r, execution, err := readInvocation(framePipe(t, valid), time.Second)
	if err != nil || r == nil || *r != inspectionRequest() || !reflect.DeepEqual(execution, Request{}) {
		t.Fatalf("inspection dispatch: %#v %#v %v", r, execution, err)
	}
	if _, err = readFrame(framePipe(t, valid), time.Second); !errors.Is(err, ErrFrame) {
		t.Fatalf("execution-only reader accepted inspection: %v", err)
	}
	for _, raw := range [][]byte{append(append([]byte(nil), valid...), 'x'), valid[:len(valid)-1], bytes.Replace(valid, []byte(InspectionVersion), []byte(strings.Repeat("x", len(InspectionVersion))), 1)} {
		r, _, err := readInvocation(framePipe(t, raw), time.Second)
		if r != nil || !errors.Is(err, ErrFrame) {
			t.Fatalf("malformed inspection dispatched: %#v %v", r, err)
		}
	}
}
func TestInspectionRegistrationReadRetainsExecutionSelectionGuards(t *testing.T) {
	p, l, r, path, before := savedLayout(t)
	got, err := loadRegistration(p, r.Key, r.LayoutID, r.PolicyDigest)
	if err != nil || got.ID != l.ID || len(got.Members) != len(l.Members) {
		t.Fatalf("scope-free existing registration: %#v %v", got, err)
	}
	invalid := r
	invalid.Scope = Scope{}
	if _, err = LoadLayout(p, invalid); !errors.Is(err, ErrRequest) {
		t.Fatalf("execution lost mandatory scope: %v", err)
	}
	for _, v := range []struct{ key, id, sha string }{{"absent", r.LayoutID, r.PolicyDigest}, {r.Key, strings.Repeat("f", 32), r.PolicyDigest}, {r.Key, r.LayoutID, strings.Repeat("f", 64)}} {
		if _, err = loadRegistration(p, v.key, v.id, v.sha); !errors.Is(err, ErrPolicy) {
			t.Fatalf("foreign registration accepted: %v", err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("registration read mutated authority: %v", err)
	}
	if _, err = os.Stat(p.DataRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registration read created layout: %v", err)
	}
}
func TestInspectionOutputIsOneBoundedFrame(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	raw := bytes.Repeat([]byte("x"), MaxLayout)
	done := make(chan error, 1)
	go func() { done <- writeInspection(w, raw, time.Second); _ = w.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if len(got) != len(raw)+4 || binary.BigEndian.Uint32(got) != MaxLayout || !bytes.Equal(got[4:], raw) {
		t.Fatal("partial or extra result frame")
	}
	for _, candidate := range [][]byte{nil, bytes.Repeat([]byte("x"), MaxLayout+1)} {
		if err = writeInspection(w, candidate, time.Second); !errors.Is(err, ErrFrame) {
			t.Fatalf("invalid output bound: %v", err)
		}
	}
}
func TestInspectionOutputTimeoutRestoresFlags(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, w.Fd(), 1031, 4096); e != 0 {
		t.Fatal(e)
	} // F_SETPIPE_SZ: force a finite undrained pipe.
	before, _, e := syscall.Syscall(syscall.SYS_FCNTL, w.Fd(), syscall.F_GETFL, 0)
	if e != 0 {
		t.Fatal(e)
	}
	err = writeInspection(w, bytes.Repeat([]byte("x"), MaxLayout), 20*time.Millisecond)
	if !errors.Is(err, ErrFrame) || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("undrained result did not time out: %v", err)
	}
	after, _, e := syscall.Syscall(syscall.SYS_FCNTL, w.Fd(), syscall.F_GETFL, 0)
	if e != 0 || before != after {
		t.Fatalf("output flags changed: %x %x %v", before, after, e)
	}
}

func TestPreparationFrameNeverFallsBackToExecution(t *testing.T) {
	original := inspectionRequest()
	request := registeredinspection.PreparationRequest{Version: registeredinspection.PreparationVersion, Key: original.Key, LayoutID: original.LayoutID, PolicyDigest: original.PolicyDigest, Member: "web"}
	raw, err := request.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	framed := make([]byte, 4)
	binary.BigEndian.PutUint32(framed, uint32(len(raw)))
	framed = append(framed, raw...)
	inspection, preparation, execution, err := readAnyInvocation(framePipe(t, framed), time.Second)
	if err != nil || inspection != nil || preparation == nil || *preparation != request || !reflect.DeepEqual(execution, Request{}) {
		t.Fatalf("invalid preparation dispatch: %v %v", preparation, err)
	}
	if _, _, err = readInvocation(framePipe(t, framed), time.Second); !errors.Is(err, ErrFrame) {
		t.Fatalf("legacy invocation accepted preparation: %v", err)
	}
	for _, bad := range [][]byte{append(append([]byte(nil), framed...), 'x'), framed[:len(framed)-1]} {
		_, preparation, _, err = readAnyInvocation(framePipe(t, bad), time.Second)
		if preparation != nil || !errors.Is(err, ErrFrame) {
			t.Fatalf("malformed preparation accepted: %v", err)
		}
	}
}

func TestInspectionLeafPreservesBoundaryErrorIdentity(t *testing.T) {
	if ErrBoundary != registeredinspection.ErrBoundary {
		t.Fatal("shared boundary sentinel identity changed")
	}
}

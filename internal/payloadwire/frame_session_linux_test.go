//go:build linux

package payloadwire

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"time"
)

func framed(t *testing.T, raw []byte) *os.File {
	t.Helper()
	frame := make([]byte, 4, 4+len(raw))
	binary.BigEndian.PutUint32(frame, uint32(len(raw)))
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	go func() { _, _ = w.Write(append(frame, raw...)); _ = w.Close() }()
	return r
}

func TestFramesNeverCrossKinds(t *testing.T) {
	s := validSession()
	session, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := Request{Version: Version, Key: s.Key, LayoutID: s.LayoutID, PolicyDigest: s.PolicyDigest, Kind: "payload", Root: true, Scope: s.Scope, Arguments: []string{"bash", "-l"}}.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectionRequest{Version: InspectionVersion, Key: s.Key, LayoutID: s.LayoutID, PolicyDigest: s.PolicyDigest}.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ReadSessionFrame(framed(t, session), time.Second); err != nil || got != s {
		t.Fatalf("session frame: %v", err)
	}
	for name, raw := range map[string][]byte{"payload": payload, "inspection": inspection} {
		if _, err := ReadSessionFrame(framed(t, raw), time.Second); !errors.Is(err, ErrFrame) {
			t.Errorf("session reader accepted a %s frame: %v", name, err)
		}
	}
	if _, _, _, err := ReadAnyInvocation(framed(t, session), time.Second); !errors.Is(err, ErrFrame) {
		t.Errorf("execution reader accepted a session frame: %v", err)
	}
}

func TestSessionFrameBoundsAndLeavesTrailingBytes(t *testing.T) {
	oversize := make([]byte, 4)
	binary.BigEndian.PutUint32(oversize, MaxSessionRequest+1)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() { _, _ = w.Write(oversize); _ = w.Close() }()
	if _, err := ReadSessionFrame(r, time.Second); !errors.Is(err, ErrFrame) {
		t.Fatalf("oversize: %v", err)
	}
	if _, err := ReadSessionFrame(framed(t, nil), time.Second); !errors.Is(err, ErrFrame) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := ReadSessionFrame(framed(t, []byte("{}")), 0); !errors.Is(err, ErrFrame) {
		t.Fatalf("zero timeout: %v", err)
	}
	// The descriptor message follows the frame on the same socket: the frame
	// reader must stop at the frame's last byte.
	raw, err := validSession().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 4, 5+len(raw))
	binary.BigEndian.PutUint32(frame, uint32(len(raw)))
	in, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	go func() { _, _ = out.Write(append(append(frame, raw...), 'F')); _ = out.Close() }()
	if _, err := ReadSessionFrame(in, time.Second); err != nil {
		t.Fatal(err)
	}
	rest := make([]byte, 2)
	if n, _ := in.Read(rest); n != 1 || rest[0] != 'F' {
		t.Fatalf("frame reader consumed the descriptor byte: %q", rest[:n])
	}
	// A reader that never gets its frame times out.
	slow, keep, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	defer keep.Close()
	if _, err := ReadSessionFrame(slow, 50*time.Millisecond); !errors.Is(err, ErrFrame) {
		t.Fatalf("timeout: %v", err)
	}
}

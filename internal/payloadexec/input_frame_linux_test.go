//go:build linux

package payloadexec

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
)

func TestFiniteInputRefusesInvalidRequestBeforeAllocation(t *testing.T) {
	if MaxPayloadInput != finiteio.MaxBytes {
		t.Fatal("helper input bound drifted from the finite I/O bound")
	}
	r := launchRequest()
	r.Input = make([]byte, MaxPayloadInput+1)
	if f, err := openPayloadInput(r); f != nil || !errors.Is(err, ErrRequest) {
		t.Fatalf("oversized input allocated: %v", err)
	}
	if f, err := openPayloadInput(launchRequest()); f != nil || err != nil {
		t.Fatalf("default EOF changed: %v", err)
	}
	if err := bindPayloadInput(nil); !errors.Is(err, ErrFrame) {
		t.Fatalf("nil input lost frame identity: %v", err)
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

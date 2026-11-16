package payloadexec

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestFinitePayloadInputEnvelopeRoundTrip(t *testing.T) {
	raw, err := launchRequest().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	input := []byte{0, 255, '\n', '{', '}'}
	encoded := base64.StdEncoding.EncodeToString(input)
	raw = append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"Input":"`+encoded+`"}`)...)
	parsed, err := ParseRequest(raw)
	if err != nil {
		t.Fatalf("finite payload input refused: %v", err)
	}
	roundTrip, err := parsed.Bytes()
	if err != nil || !bytes.Equal(raw, roundTrip) {
		t.Fatalf("opaque input changed: %v", err)
	}
}

func TestPayloadInputPreservesDefaultAndRefusesWrongKindBoundsAndEncoding(t *testing.T) {
	r := launchRequest()
	raw, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"Input"`)) {
		t.Fatal("default encoding changed")
	}
	r.Input = bytes.Repeat([]byte{255}, MaxPayloadInput)
	raw, err = r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= MaxPayloadInput || len(raw) > MaxRequest {
		t.Fatalf("base64 envelope bound: %d", len(raw))
	}
	parsed, err := ParseRequest(raw)
	if err != nil || !bytes.Equal(parsed.Input, r.Input) {
		t.Fatalf("maximum input: %v", err)
	}
	r.Input = append(r.Input, 0)
	if _, err := r.Bytes(); !errors.Is(err, ErrRequest) {
		t.Fatalf("oversized input: %v", err)
	}
	r = launchRequest()
	r.Kind = "git"
	r.Arguments = []string{"git", "status"}
	r.Input = []byte{1}
	if _, err := r.Bytes(); !errors.Is(err, ErrRequest) {
		t.Fatalf("Git input field accepted: %v", err)
	}
	original, err := launchRequest().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"Input":null`, `"Input":""`, `"Input":"!"`, `"Input":[1]`} {
		candidate := append(append([]byte(nil), original[:len(original)-1]...), []byte(","+field+"}")...)
		if _, err := ParseRequest(candidate); !errors.Is(err, ErrRequest) {
			t.Fatalf("noncanonical input %s: %v", field, err)
		}
	}
	r = launchRequest()
	r.Input = []byte{1}
	r.Arguments = []string{"sh", strings.Repeat("x", MaxRequest)}
	if _, err := r.Bytes(); !errors.Is(err, ErrRequest) {
		t.Fatalf("total envelope limit: %v", err)
	}
}

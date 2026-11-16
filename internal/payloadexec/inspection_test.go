package payloadexec

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func inspectionRequest() InspectionRequest {
	return InspectionRequest{InspectionVersion, "example", "0123456789abcdef0123456789abcdef", strings.Repeat("a", 64)}
}

func TestInspectionIsDistinctCanonicalScopeFreeAction(t *testing.T) {
	r := inspectionRequest()
	raw, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseInspectionRequest(raw)
	if err != nil || got != r {
		t.Fatalf("canonical inspection: %#v %v", got, err)
	}
	if _, err = ParseRequest(raw); !errors.Is(err, ErrRequest) {
		t.Fatalf("inspection accepted as execution: %v", err)
	}
	for _, extra := range []string{`"Scope":{}`, `"Arguments":[]`, `"UID":0`, `"path":"/data"`, `"lease_token":"private"`, `"Kind":"git"`, `"Version":"superbench.payload-inspection.v1"`} {
		candidate := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(","+extra+"}")...)
		if _, err = ParseInspectionRequest(candidate); !errors.Is(err, ErrRequest) {
			t.Fatalf("inspection accepted extra authority %s: %v", extra, err)
		}
	}
	for _, change := range []func(*InspectionRequest){func(r *InspectionRequest) { r.Version = Version }, func(r *InspectionRequest) { r.Key = "../data" }, func(r *InspectionRequest) { r.LayoutID = strings.Repeat("a", 31) }, func(r *InspectionRequest) { r.PolicyDigest = strings.Repeat("A", 64) }} {
		candidate := r
		change(&candidate)
		if _, err = candidate.Bytes(); !errors.Is(err, ErrRequest) {
			t.Fatalf("invalid inspection accepted: %v", err)
		}
	}
	for _, candidate := range [][]byte{[]byte("null"), bytes.Replace(raw, []byte(`"Key":"example"`), []byte(`"Key":null`), 1), append(raw, 'x'), bytes.Repeat([]byte(" "), MaxInspectionRequest+1)} {
		if _, err = ParseInspectionRequest(candidate); !errors.Is(err, ErrRequest) {
			t.Fatalf("malformed inspection accepted: %v", err)
		}
	}
	execution, err := launchRequest().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseInspectionRequest(execution); !errors.Is(err, ErrRequest) {
		t.Fatalf("execution accepted as inspection: %v", err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil || len(fields) != 4 {
		t.Fatalf("unexpected inspection shape %s: %v", raw, err)
	}
}

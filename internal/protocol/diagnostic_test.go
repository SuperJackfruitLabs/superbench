package protocol

import (
	"errors"
	"strings"
	"testing"
)

func TestShapeDiagnosticUsesOnlySchemaNames(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"version":"superbench.v1","operation":"status","request_id":"PRIVATE"}`, "unexpected field request_id for operation status"},
		{`{"version":"superbench.v1","operation":"status","PRIVATE-FIELD":"PRIVATE"}`, "unexpected field for operation status"},
		{`{"version":"superbench.v1","operation":"PRIVATE-OP"}`, "invalid operation request"},
		{`{"version":"superbench.v1","operation":"status","request_id":"a","request_id":"b"}`, "invalid operation request"},
	} {
		_, err := Parse([]byte(tc.raw))
		if err == nil || InvalidMessage(err) != tc.want {
			t.Fatalf("wanted %q, got %v/%q", tc.want, err, InvalidMessage(err))
		}
	}
	if got := InvalidMessage(errors.New(strings.Repeat("PRIVATE", 1000))); got != "invalid operation request" {
		t.Fatalf("arbitrary error exposed: %q", got)
	}
	// Stable schema order makes multi-field diagnostics independent of map iteration.
	for range 100 {
		_, err := Parse([]byte(`{"version":"superbench.v1","operation":"status","request_id":"r","lease_token":"secret"}`))
		if got := InvalidMessage(err); got != "unexpected field lease_token for operation status" {
			t.Fatal(got)
		}
	}
}

package main

import (
	"strings"
	"testing"
)

func TestOperationCommandsRejectMalformedInputBeforePolicyRead(t *testing.T) {
	h := strings.Repeat("a", 64)
	e := strings.Repeat("b", 32)
	for _, args := range [][]string{
		{"operation-info", "extra"},
		{"apply-operation", "bench", "card", "0", h, e, h, h},
		{"apply-operation", "bench", "../escape", "0", h, e, h, h, h},
		{"apply-operation", "shared", "store", "1024", h, e, h, h, h},
		{"apply-operation", "bench", "card", "01", h, e, h, h, h},
		{"apply-operation", "bench", "card", "1025", h, e, h, h, h},
		{"apply-operation", "bench", "card", "0", strings.ToUpper(h), e, h, h, h},
		{"cancel-operation", h, e, h, h, ""},
	} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "invalid quota operation") {
			t.Fatalf("invalid command reached policy/effects: %q: %v", args, err)
		}
	}
}

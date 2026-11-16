package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandoffCLIRejectsRemoteCapabilitiesAndIrrelevantFlagsBeforeEffects(t *testing.T) {
	for _, args := range [][]string{
		{"offer", "--creation-id=x", "bad/key"},
		{"offer", "--socket=", "--creation-id=x", "card"},
		{"offer", "--token=", "--creation-id=x", "card"},
		{"offer", "--artifact=unused", "--creation-id=x", "card"},
		{"begin-export", "--plan=missing", "--approve=wrong", "card"},
		{"import", "--artifact=missing", "--approve=wrong", "card"},
	} {
		var out, stderr bytes.Buffer
		state := filepath.Join(t.TempDir(), "state")
		args = append([]string{args[0], "--state=" + state}, args[1:]...)
		if err := handoffCLIContext(context.Background(), args, &out, &stderr); err == nil {
			t.Fatal("malformed local handoff accepted", args)
		}
		if out.Len() != 0 {
			t.Fatal("failed preflight emitted retained output", out.String())
		}
	}
	t.Setenv("BENCH_LEASE_TOKEN", "ambient-private-capability")
	var out, stderr bytes.Buffer
	_ = handoffCLIContext(context.Background(), []string{"offer", "--help"}, &out, &stderr)
	if strings.Contains(stderr.String(), "ambient-private-capability") {
		t.Fatal("handoff help disclosed ambient capability")
	}
}

package main

import (
	"encoding/json"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"testing"
)

func TestLocalScalarPresentationKeepsPreRegistrationShape(t *testing.T) {
	b := &bench.Bench{Key: "card", Repo: "repo", Branch: "task", Path: "/data/benches/card/worktree", Mirror: "mirror", Checkpoint: "sealed", Lease: &bench.Lease{Holder: "owner", Token: "existing-grant"}}
	encode := func(v any) string {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	old := encode(b)
	if encode(scalarOutput(b)) != old {
		t.Fatal("legacy scalar fields changed")
	}
	b.RegistrationID = "0123456789abcdef0123456789abcdef"
	b.PayloadLayoutVersion = "superbench.payload-layout.v1"
	b.AllocationRoot = "/data/benches/card"
	if encode(scalarOutput(b)) != old {
		t.Fatal("private authority leaked into scalar CLI JSON")
	}
	for _, v := range []any{[]bench.Bench{*b}, &bench.RunResult{Bench: b}} {
		raw := encode(scalarOutput(v))
		var fields any
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			t.Fatal(err)
		}
		if containsPrivate(fields) {
			t.Fatal("nested output leaked storage authority", raw)
		}
	}
	if encode(b) == old {
		t.Fatal("presentation mutated durable storage")
	}
}
func containsPrivate(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, y := range x {
			if k == "RegistrationID" || k == "PayloadLayoutVersion" || k == "AllocationRoot" || containsPrivate(y) {
				return true
			}
		}
	case []any:
		for _, y := range x {
			if containsPrivate(y) {
				return true
			}
		}
	}
	return false
}

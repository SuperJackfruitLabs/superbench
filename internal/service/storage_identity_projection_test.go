package service

import (
	"encoding/json"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/scalarapi"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceapi"
)

func TestStorageRegistrationDoesNotChangeExistingWireProjections(t *testing.T) {
	b := &bench.Bench{Key: "card", Repo: "https://example.invalid/repo", Branch: "task", Path: "/data/benches/card/worktree", Checkpoint: "checkpoint"}
	encode := func(v any) string {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	v1, v2 := encode(scalarapi.BenchView(b, false)), encode(workspaceapi.Inventory(b))
	b.RegistrationID = "0123456789abcdef0123456789abcdef"
	b.PayloadLayoutVersion = "superbench.payload-layout.v1"
	b.AllocationRoot = "/data/benches/card"
	if encode(scalarapi.BenchView(b, false)) != v1 || encode(workspaceapi.Inventory(b)) != v2 {
		t.Fatal("internal storage identity changed closed v1/v2 wire projection")
	}
	// The storage codec must retain the binding even though public DTOs omit it.
	var stored bench.Bench
	if err := json.Unmarshal([]byte(encode(b)), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.RegistrationID != b.RegistrationID || stored.PayloadLayoutVersion != b.PayloadLayoutVersion || stored.AllocationRoot != b.AllocationRoot {
		t.Fatal("storage roundtrip lost execution registration")
	}
}

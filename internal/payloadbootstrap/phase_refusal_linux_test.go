//go:build linux

package payloadbootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
)

func TestMaterializeRequiresBegunPhase(t *testing.T) {
	if err := (intent{Phase: "begun"}).requireMaterializePhase(); err != nil {
		t.Fatalf("begun phase refused: %v", err)
	}
	for _, phase := range []string{"", "allocating", "staging", "materialized_unfenced", "staged", "transferring", "completed", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			if err := (intent{Phase: phase}).requireMaterializePhase(); !errors.Is(err, ErrBootstrap) {
				t.Fatalf("nonbegun Materialize phase accepted: phase=%q err=%v", phase, err)
			}
		})
	}
}

func TestClosedRequiresCompletedMaterializationOnCurrentHostAndBoot(t *testing.T) {
	current, err := execution.CurrentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{digest: strings.Repeat("c", 64)}
	m.storage.Ledger = t.TempDir()
	spec := validSpecForTest()
	raw, err := spec.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	valid := intent{Spec: spec, Phase: "staged", CommandCompleted: true,
		Materialized: &materialized{SpecSHA256: hex.EncodeToString(sum[:]), PolicyDigest: m.digest, HostID: current.HostID, BootID: current.BootID}}
	// This is only a metadata refusal test. Matching metadata must reach the real
	// missing-controls filesystem lookup, never serve as fabricated authority.
	var missing *os.PathError
	if err = m.closed(valid); !errors.As(err, &missing) || !errors.Is(err, os.ErrNotExist) || missing.Path != m.controlState(spec) {
		t.Fatalf("valid metadata did not reach missing controls: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*intent)
	}{
		{"phase", func(i *intent) { i.Phase = "begun" }},
		{"nil-materialized", func(i *intent) { i.Materialized = nil }},
		{"incomplete-command", func(i *intent) { i.CommandCompleted = false }},
		{"host-mismatch", func(i *intent) { i.Materialized.HostID += "-foreign" }},
		{"boot-mismatch", func(i *intent) { i.Materialized.BootID += "-foreign" }},
		{"spec-mismatch", func(i *intent) { i.Materialized.SpecSHA256 = strings.Repeat("d", 64) }},
		{"policy-mismatch", func(i *intent) { i.Materialized.PolicyDigest = strings.Repeat("e", 64) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := valid
			proof := *valid.Materialized
			candidate.Materialized = &proof
			tc.mutate(&candidate)
			before := candidate
			if candidate.Materialized != nil {
				copy := *candidate.Materialized
				before.Materialized = &copy
			}
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("closed refusal panicked: %v", recovered)
				}
			}()
			if err := m.closed(candidate); !errors.Is(err, ErrBootstrap) {
				t.Fatalf("closed accepted invalid %s metadata: %v", tc.name, err)
			}
			if !reflect.DeepEqual(candidate, before) {
				t.Fatal("refusal changed original intent")
			}
		})
	}
	entries, err := os.ReadDir(m.storage.Ledger)
	if err != nil || len(entries) != 0 {
		t.Fatalf("metadata refusal created control state: %v %v", entries, err)
	}
}

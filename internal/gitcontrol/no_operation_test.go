package gitcontrol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestNoOperationProofRefusesAmbiguityWithoutChangingEvidence(t *testing.T) {
	for _, kind := range []string{"exact", "legacy", "unknown", "other"} {
		t.Run(kind, func(t *testing.T) {
			state := canonicalTemp(t)
			path, j := fixtureIntent(t, state, state)
			target := operationcontrol.Target{UID: 7, RequestID: "not-admitted", Digest: strings.Repeat("a", 64)}
			j.OperationID = target.ID()
			j.OperationEpoch = strings.Repeat("b", 32)
			if kind == "legacy" {
				j.Version = legacyControlVersion
				j.OperationID = ""
				j.OperationEpoch = ""
			}
			if kind == "other" {
				j.OperationID = strings.Repeat("c", 64)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := saveControl(filepath.Dir(path), j); err != nil {
				t.Fatal(err)
			}
			if kind == "unknown" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), "unknown"), []byte("retained"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			err = NoOperation(state, target)
			if (err == nil) != (kind == "other") {
				t.Fatal(kind, err)
			}
			after, e := os.ReadFile(path)
			if e != nil || string(after) != string(before) {
				t.Fatal("inspection changed journal", e)
			}
		})
	}
}

package freshrecovery

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshgeneration"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
)

// Root's answer binds the Spec the daemon sent on stdin to the intent it
// read: another workspace's or another vector's Spec is refused, even when
// root's retirement of the intent it read is complete.
func TestRetiredInspectionBindsTheSpecOnStdin(t *testing.T) {
	root, authority := "/srv/data/benches/card", strings.Repeat("a", 64)
	s := payloadbootstrap.Spec{Version: payloadbootstrap.Version, Key: "card", CheckpointRepository: "api", ID: strings.Repeat("1", 32),
		Members: []payloadbootstrap.Member{{Name: "api", Repository: "https://example.invalid/repo.git", Branch: "card/task", Source: strings.Repeat("b", 40), ObjectFormat: "sha1"}}}
	raw, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	recorded := freshgeneration.Allocation{Path: root, ProjectID: 10004, Bytes: 1 << 30, Inodes: 4096}
	prior := freshgeneration.Prior{Key: "card", ID: s.ID, Phase: "staging", Authority: authority, Path: root, ProjectID: 10004, Bytes: recorded.Bytes, Inodes: recorded.Inodes,
		Operation: "op", PolicyDigest: "policy", IntentSHA256: strings.Repeat("c", 64), SpecSHA256: hex.EncodeToString(sum[:])}
	prior.Retirement = &freshgeneration.Retirement{Key: "card", ID: s.ID, Phase: "staging", IntentSHA256: prior.IntentSHA256, SpecSHA256: prior.SpecSHA256, Operation: "op", PolicyDigest: "policy",
		OperationDenied: true, Recorded: recorded, ApprovalKey: "card", Authority: authority, Approved: recorded}
	if err = retiredSpec(prior, s, authority, root); err != nil {
		t.Fatal(err)
	}
	other := s
	other.Members = append([]payloadbootstrap.Member{}, s.Members...)
	other.Members[0].Source = strings.Repeat("e", 40)
	another := s
	another.ID = strings.Repeat("2", 32)
	for name, spec := range map[string]payloadbootstrap.Spec{"another vector": other, "another workspace": another, "invalid": {}} {
		if err = retiredSpec(prior, spec, authority, root); !errors.Is(err, ErrRecovery) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	prior.Retirement.OperationDenied = false
	if err = retiredSpec(prior, s, authority, root); !errors.Is(err, freshgeneration.ErrGeneration) {
		t.Fatalf("an unretired intent accepted: %v", err)
	}
	t.Log("STUCK_CREATE_ROOT_BINDS_STDIN_SPEC_PASS")
}

//go:build linux

package payloadbootstrap

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"strings"
	"testing"
)

func reservationFixture(t *testing.T) (*Manager, intent) {
	t.Helper()
	p, s, r := resultFixture(t)
	i := quotaoperation.Identity{OperationID: strings.Repeat("a", 64), Epoch: strings.Repeat("b", 32), StateIdentity: strings.Repeat("c", 64), Authority: strings.Repeat("d", 64), BinarySHA256: strings.Repeat("e", 64)}
	m := &Manager{policy: p, storage: hoststorage.Policy{BenchBytes: s.QuotaBytes, BenchInodes: r.Allocation.Inodes}, operation: i, digest: r.Layout.PolicyDigest}
	return m, intent{Spec: s, Operation: i, PolicyDigest: m.digest, Phase: "reserved"}
}

func TestReservedIntentBindsProvenanceWithoutClaimingEffects(t *testing.T) {
	m, valid := reservationFixture(t)
	if err := m.validateIntent(valid.Spec, valid); err != nil {
		t.Fatal("valid reservation refused", err)
	}
	cases := map[string]func(*intent){
		"allocation":        func(i *intent) { i.Allocation.ProjectID = 10000 },
		"root":              func(i *intent) { i.Root.Inode = 1 },
		"controls":          func(i *intent) { i.Controls.Inode = 1 },
		"materialized":      func(i *intent) { i.Materialized = &materialized{} },
		"command":           func(i *intent) { i.CommandSHA256 = strings.Repeat("f", 64) },
		"completed-command": func(i *intent) { i.CommandCompleted = true },
		"spec":              func(i *intent) { i.Spec.ID = strings.Repeat("f", 32) },
		"operation":         func(i *intent) { i.Operation.Epoch = strings.Repeat("f", 32) },
		"policy":            func(i *intent) { i.PolicyDigest = strings.Repeat("f", 64) },
		"premature-begun":   func(i *intent) { i.Phase = "begun" },
		"unknown-phase":     func(i *intent) { i.Phase = "unknown" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			i := valid
			change(&i)
			if err := m.validateIntent(valid.Spec, i); !errors.Is(err, ErrBootstrap) {
				t.Fatalf("reservation claimed %s: %v", name, err)
			}
		})
	}
}

func TestRetainedIntentRequiresExactAllocationAfterReservation(t *testing.T) {
	m, i := reservationFixture(t)
	_, _, r := resultFixture(t)
	i.Phase = "begun"
	i.Allocation = r.Allocation
	i.Root = payloadexec.Node{Device: 8, Inode: 1}
	i.Controls = payloadexec.Node{Device: 8, Inode: 2}
	if err := m.validateIntent(i.Spec, i); err != nil {
		t.Fatal("valid retained allocation refused", err)
	}
	for _, phase := range []string{"allocating", "begun", "staging", "materialized_unfenced", "staged", "transferring", "completed"} {
		t.Run(phase, func(t *testing.T) {
			candidate := i
			candidate.Phase = phase
			if err := m.validateIntent(i.Spec, candidate); err != nil {
				t.Fatal(err)
			}
			candidate.Allocation.Bytes *= 2
			if err := m.validateIntent(i.Spec, candidate); !errors.Is(err, ErrBootstrap) {
				t.Fatal("changed retained quota accepted", err)
			}
		})
	}
}

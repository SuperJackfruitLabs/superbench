//go:build linux

package payloadbootstrap

import (
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

func TestRetainedBootstrapAllocationMustMatchActualPolicyAndRequest(t *testing.T) {
	p, s, r := resultFixture(t)
	m := Manager{policy: p, storage: hoststorage.Policy{BenchBytes: 16 << 20, BenchInodes: r.Allocation.Inodes}}
	if !m.validAllocation(s, r.Allocation) {
		t.Fatal("explicit allocation refused")
	}
	s.QuotaBytes = 0
	if m.validAllocation(s, r.Allocation) {
		t.Fatal("default policy cap not enforced")
	}
	r.Allocation.Bytes = m.storage.BenchBytes
	if !m.validAllocation(s, r.Allocation) {
		t.Fatal("default policy allocation refused")
	}
	m.storage.BenchInodes++
	if m.validAllocation(s, r.Allocation) {
		t.Fatal("changed policy inode cap accepted")
	}
}

//go:build linux

package payloadbootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func retirementFixture(t *testing.T) (*Manager, Spec, RetirementPlan) {
	t.Helper()
	_, s, r := resultFixture(t)
	p := hoststorage.Policy{OperationJournal: "indexed-v2", Root: "/srv/superbench-data", Ledger: "/var/lib/superbench/quota", OwnerUID: 1001, OwnerGID: 1001,
		BenchBytes: 32 << 20, StoreBytes: 16 << 20, MirrorBytes: 16 << 20, ReserveBytes: 16 << 20, BenchInodes: 4096, StoreInodes: 4096, MirrorInodes: 4096, ReserveInodes: 4096}
	h := strings.Repeat("a", 64)
	i := quotaoperation.Identity{OperationID: h, Epoch: strings.Repeat("b", 32), StateIdentity: strings.Repeat("c", 64), Authority: h, BinarySHA256: h}
	m := &Manager{storage: p, digest: h, operation: i}
	raw, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	plan := RetirementPlan{Version: RetirementVersion, Bootstrap: Inspection{Version: InspectionVersion, Key: s.Key, ID: s.ID, SpecSHA256: digestBytes(raw), IntentSHA256: h, PolicyDigest: h, Phase: "completed", Operation: i, OperationDenied: true, RecordedAllocation: r.Allocation},
		Storage: freshretirement.Approval{Version: freshretirement.Version, Key: s.Key, Authority: h, Assignment: r.Allocation, Parent: freshretirement.Parent{Device: 8, Inode: 3, Mode: uint32(os.ModeDir | 0711)}}}
	return m, s, plan
}

func TestRetirementPlanRequiresOriginalDeniedProvenanceAndSeparateAllocation(t *testing.T) {
	m, s, p := retirementFixture(t)
	if err := p.validate(s, m); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*RetirementPlan){
		"version": func(p *RetirementPlan) { p.Version = "" }, "denial": func(p *RetirementPlan) { p.Bootstrap.OperationDenied = false },
		"operation": func(p *RetirementPlan) { p.Bootstrap.Operation.StateIdentity = strings.Repeat("f", 64) },
		"intent":    func(p *RetirementPlan) { p.Bootstrap.IntentSHA256 = "" }, "spec": func(p *RetirementPlan) { p.Bootstrap.SpecSHA256 = strings.Repeat("f", 64) },
		"policy": func(p *RetirementPlan) { p.Bootstrap.PolicyDigest = strings.Repeat("f", 64) }, "key": func(p *RetirementPlan) { p.Bootstrap.Key = "other" },
		"id": func(p *RetirementPlan) { p.Bootstrap.ID = strings.Repeat("f", 32) }, "authority": func(p *RetirementPlan) { p.Storage.Authority = strings.Repeat("f", 64) },
		"parent": func(p *RetirementPlan) { p.Storage.Parent.UID = 1001 }, "quota": func(p *RetirementPlan) { p.Storage.Assignment.Bytes *= 2 },
		"allocation": func(p *RetirementPlan) { p.Bootstrap.RecordedAllocation.ProjectID++ },
	} {
		t.Run(name, func(t *testing.T) {
			bad := p
			change(&bad)
			if err := bad.validate(s, m); !errors.Is(err, ErrBootstrap) {
				t.Fatal("foreign plan accepted", err)
			}
		})
	}
	reserved := p
	reserved.Bootstrap.Phase = "reserved"
	reserved.Bootstrap.RecordedAllocation = hoststorage.Assignment{}
	if err := reserved.validate(s, m); err != nil {
		t.Fatal("zero historical allocation refused", err)
	}
	reserved.Bootstrap.RecordedAllocation = p.Storage.Assignment
	if err := reserved.validate(s, m); !errors.Is(err, ErrBootstrap) {
		t.Fatal("invented historical assignment accepted", err)
	}
}

func TestReservedRetirementDefaultQuotaMustMatchOriginalPolicy(t *testing.T) {
	m, s, p := retirementFixture(t)
	s.QuotaBytes = 0
	raw, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	p.Bootstrap.SpecSHA256 = digestBytes(raw)
	p.Bootstrap.Phase = "reserved"
	p.Bootstrap.RecordedAllocation = hoststorage.Assignment{}
	p.Storage.Assignment.Bytes = m.storage.BenchBytes
	if err := p.validate(s, m); err != nil {
		t.Fatal(err)
	}
	p.Storage.Assignment.Bytes /= 2
	if err := p.validate(s, m); !errors.Is(err, ErrBootstrap) {
		t.Fatal("changed default reservation accepted", err)
	}
}

func TestRetirementRequestIsBoundedAndCanonicalBeforeAuthority(t *testing.T) {
	_, s, p := retirementFixture(t)
	raw, err := json.Marshal(struct {
		Spec Spec
		Plan RetirementPlan
	}{s, p})
	if err != nil {
		t.Fatal(err)
	}
	got, plan, err := ParseRetirement(raw)
	if err != nil || got.Key != s.Key || plan != p {
		t.Fatalf("valid request: %+v %v", plan, err)
	}
	for _, bad := range [][]byte{[]byte("null"), append(raw, raw...), []byte(strings.Replace(string(raw), `"Spec":`, `"extra":1,"Spec":`, 1)), []byte(strings.Replace(string(raw), `"Plan":`, `"Plan":null,"Plan":`, 1)), make([]byte, MaxRetirement+1)} {
		if _, _, err := ParseRetirement(bad); !errors.Is(err, ErrBootstrap) {
			t.Fatal("malformed request accepted", err)
		}
	}
}

func TestRetirementRequiresActualInstalledManagerBeforeEffects(t *testing.T) {
	_, s, p := retirementFixture(t)
	for _, m := range []*Manager{nil, {}, {digest: "invented"}} {
		if got, err := m.PlanRetirement(context.Background(), s); !errors.Is(err, ErrBootstrap) || got != (RetirementPlan{}) {
			t.Fatal(got, err)
		}
		if got, err := m.RetireAbsent(context.Background(), s, p); !errors.Is(err, ErrBootstrap) || got != (hoststorage.Assignment{}) {
			t.Fatal(got, err)
		}
	}
}

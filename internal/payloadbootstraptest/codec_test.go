package payloadbootstraptest

// The bootstrap spec and result codecs, tested through their exported API
// outside the package, which keeps the package within its size bound.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	. "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

func validSpecForTest() Spec {
	return Spec{Version: Version, Key: "card", CheckpointRepository: "api", ID: strings.Repeat("a", 32), Members: []Member{{Name: "api", Repository: "https://example.invalid/repo.git", Branch: "card/task", Source: strings.Repeat("b", 40), ObjectFormat: "sha1"}}}
}
func TestFreshSpecificationRejectsAmbiguousOrNoncanonicalAuthority(t *testing.T) {
	valid := validSpecForTest()
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Spec){
		func(s *Spec) { s.ID = "card" }, func(s *Spec) { s.Key = "../card" },
		func(s *Spec) { s.Members[0].Name = ".superbench-git" },
		func(s *Spec) { s.Members[0].Source = "HEAD" }, func(s *Spec) { s.Members[0].ObjectFormat = "sha256" },
		func(s *Spec) { s.Members[0].Branch = "--upload-pack=bad" },
		func(s *Spec) { s.Members = append(s.Members, s.Members[0]) },
	} {
		s := valid
		s.Members = append([]Member(nil), valid.Members...)
		mutate(&s)
		if err := s.Validate(); !errors.Is(err, ErrBootstrap) {
			t.Fatalf("accepted invalid spec %#v: %v", s, err)
		}
	}
	raw, err := valid.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseSpec(append(raw, []byte(` {}`)...)); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("trailing document: %v", err)
	}
	if _, err = ParseSpec([]byte(strings.Replace(string(raw), `"Version":`, `"Unknown":0,"Version":`, 1))); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("unknown property: %v", err)
	}
}

func TestFreshSpecificationCodecBindsCanonicalVector(t *testing.T) {
	valid := validSpecForTest()
	raw, err := valid.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSpec(raw)
	if err != nil || got.ID != valid.ID || len(got.Members) != 1 {
		t.Fatalf("valid specification refused: %#v %v", got, err)
	}
	// The file parser deliberately permits formatting whitespace, but not a
	// second representation with unknown, duplicate or reordered authority fields.
	if _, err := ParseSpec(append([]byte(" \n"), append(raw, '\n')...)); err != nil {
		t.Fatalf("formatting compatibility changed: %v", err)
	}
	for _, bad := range [][]byte{
		[]byte(strings.Replace(string(raw), `"Version":`, `"Version":"`+Version+`","Version":`, 1)),
		[]byte(strings.Replace(string(raw), `"Members":[`, `"Members":null,"Extra":[`, 1)),
		[]byte(strings.Replace(string(raw), `"Key":"card"`, `"Key":null`, 1)),
		append(append([]byte{}, raw...), []byte(` {}`)...),
		[]byte(strings.Replace(string(raw), `"Version":"`+Version+`","Key":"card"`, `"Key":"card","Version":"`+Version+`"`, 1)),
	} {
		if _, err := ParseSpec(bad); !errors.Is(err, ErrBootstrap) {
			t.Fatalf("ambiguous specification accepted: %s: %v", bad, err)
		}
	}
}

// A v1 spec is a key's first generation with unchanged bytes; only v2 names a
// later generation and the reclaimed workspace it follows.
func TestGenerationSpecIsVersioned(t *testing.T) {
	if raw, err := validSpecForTest().Bytes(); err != nil || strings.Contains(string(raw), "Generation") || strings.Contains(string(raw), "Previous") {
		t.Fatalf("v1 bytes changed: %s %v", raw, err)
	}
	second := validSpecForTest()
	second.Version, second.ID, second.Generation, second.Previous = GenerationVersion, strings.Repeat("e", 32), 2, strings.Repeat("a", 32)
	raw, err := second.Bytes()
	if got, perr := ParseSpec(raw); err != nil || perr != nil || got.Generation != 2 || got.Previous != second.Previous || Stem(got) != "card@2" || Stem(validSpecForTest()) != "card" {
		t.Fatalf("v2 round trip: %+v %v %v", got, err, perr)
	}
	for name, change := range map[string]func(*Spec){
		"v1 naming a generation": func(s *Spec) { s.Version = Version },
		"v1 naming a previous":   func(s *Spec) { s.Version, s.Generation = Version, 0 },
		"v2 first generation":    func(s *Spec) { s.Generation = 0 },
		"unknown version":        func(s *Spec) { s.Version = "superbench.payload-bootstrap.v3" },
	} {
		s := second
		change(&s)
		if err := s.Validate(); !errors.Is(err, ErrBootstrap) {
			t.Errorf("%s: accepted", name)
		}
	}
}

func resultFixture(t *testing.T) (payloadexec.Policy, Spec, Result) {
	t.Helper()
	p := payloadexec.Policy{DaemonUID: 1001, DaemonGID: 1001, PayloadUID: 1002, PayloadGID: 1002, DataRoot: "/srv/superbench-data", StateRoot: "/var/lib/superbench", CgroupRoot: "/sys/fs/cgroup/superbench"}
	raw, err := p.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	s := validSpecForTest()
	s.QuotaBytes = 8 << 20
	l := payloadexec.Layout{Version: payloadexec.LayoutVersion, Key: s.Key, ID: s.ID, PolicyDigest: hex.EncodeToString(sum[:]), Root: payloadexec.Node{Device: 8, Inode: 1}, RootHome: payloadexec.Node{Device: 8, Inode: 2}, RootTemp: payloadexec.Node{Device: 8, Inode: 3}, Members: []payloadexec.LayoutMember{{Name: "api", Checkout: payloadexec.Node{Device: 8, Inode: 4}, Admin: payloadexec.Node{Device: 8, Inode: 5}, Home: payloadexec.Node{Device: 8, Inode: 6}, Temp: payloadexec.Node{Device: 8, Inode: 7}}}}
	return p, s, Result{Version: ResultVersion, Layout: l, Allocation: quotahelper.Assignment{Path: p.DataRoot + "/benches/card", ProjectID: 10000, Bytes: s.QuotaBytes, Inodes: 4096}}
}

func TestBootstrapResultBindsOriginalAllocationAndGroupedRegistration(t *testing.T) {
	p, s, valid := resultFixture(t)
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseResult(raw, p, s)
	if err != nil || got.Allocation != valid.Allocation || got.Layout.ID != s.ID {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for name, change := range map[string]func(*Result){
		"version":        func(r *Result) { r.Version = "layout" },
		"bid":            func(r *Result) { r.Layout.ID = strings.Repeat("f", 32) },
		"key":            func(r *Result) { r.Layout.Key = "other" },
		"member":         func(r *Result) { r.Layout.Members[0].Name = "web" },
		"scalar":         func(r *Result) { r.Layout.Scalar = true },
		"policy":         func(r *Result) { r.Layout.PolicyDigest = strings.Repeat("e", 64) },
		"path":           func(r *Result) { r.Allocation.Path = p.DataRoot + "/benches/other" },
		"shared-project": func(r *Result) { r.Allocation.ProjectID = 100 },
		"request-cap":    func(r *Result) { r.Allocation.Bytes *= 2 },
		"zero-inodes":    func(r *Result) { r.Allocation.Inodes = 0 },
		"inode-overflow": func(r *Result) { r.Allocation.Inodes = 1 << 41 },
	} {
		t.Run(name, func(t *testing.T) {
			r := valid
			r.Layout.Members = append([]payloadexec.LayoutMember(nil), valid.Layout.Members...)
			change(&r)
			b, e := json.Marshal(r)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = ParseResult(b, p, s); !errors.Is(e, ErrBootstrap) {
				t.Fatalf("bad allocation accepted: %v", e)
			}
		})
	}
	for _, bad := range [][]byte{
		[]byte(strings.Replace(string(raw), `"Version":`, `"Unknown":0,"Version":`, 1)),
		[]byte(strings.Replace(string(raw), `"Allocation":`, `"Allocation":null,"Allocation":`, 1)),
		append(append([]byte{}, raw...), []byte(` {}`)...),
		[]byte(`null`),
		make([]byte, quotahelper.MaxReply+1),
	} {
		if _, e := ParseResult(bad, p, s); !errors.Is(e, ErrBootstrap) {
			t.Fatalf("noncanonical result accepted: %v", e)
		}
	}
}

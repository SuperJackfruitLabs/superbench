package serviceidentity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

func codecRegistry(p payloadpolicy.Policy) Registry {
	raw, _ := p.Bytes()
	digest := sha256.Sum256(raw)
	return Registry{Version: Version, LayoutVersion: payloadpolicy.LayoutVersion, Policy: p, PolicySHA256: hex.EncodeToString(digest[:])}
}

func TestRegistryCanonicalPolicyAndEnvelope(t *testing.T) {
	p := payloadpolicy.Policy{DaemonUID: 1001, DaemonGID: 1003, PayloadUID: 1002, PayloadGID: 1004, DataRoot: "/data", StateRoot: "/state", CgroupRoot: "/cgroup"}
	r := codecRegistry(p)
	const policy = `{"DaemonUID":1001,"DaemonGID":1003,"PayloadUID":1002,"PayloadGID":1004,"DataRoot":"/data","StateRoot":"/state","CgroupRoot":"/cgroup"}`
	const digest = "8c937daed89c867982895d14d28ddf6e48c3b10d7209516b9264eaff5a7b2bb7"
	const golden = `{"Version":"superbench.service-identity.v1","LayoutVersion":"superbench.payload-layout.v1","Policy":` + policy + `,"PolicySHA256":"` + digest + `"}`
	raw, err := r.Bytes()
	if err != nil || string(raw) != golden || r.PolicySHA256 != digest {
		t.Fatalf("canonical registry changed: %q %v", raw, err)
	}
	for _, input := range [][]byte{raw, []byte(" \n" + golden + "\t"), bytes.ReplaceAll(raw, []byte(","), []byte(",\n\t"))} {
		got, err := ParseRegistry(input)
		if err != nil || got != r {
			t.Fatalf("canonical/whitespace registry: %#v %v", got, err)
		}
	}
	if MaxRegistry != payloadpolicy.MaxPolicy+1024 || FixedRegistry != "/etc/superbench/service-identity.json" {
		t.Fatal("fixed registry bounds/path changed")
	}
}

func TestRegistryStrictCanonicalRefusals(t *testing.T) {
	p := payloadpolicy.Policy{DaemonUID: 1001, DaemonGID: 1003, PayloadUID: 1002, PayloadGID: 1004, DataRoot: "/data", StateRoot: "/state", CgroupRoot: "/cgroup"}
	r := codecRegistry(p)
	raw, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	policyRaw, err := p.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	changed := func(old, next string) []byte { return bytes.Replace(raw, []byte(old), []byte(next), 1) }
	cases := map[string][]byte{
		"unknown":            append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"Unknown":true}`)...),
		"duplicate":          changed(`"Version":`, `"Version":"superbench.service-identity.v1","Version":`),
		"reordered":          changed(`"Version":"superbench.service-identity.v1","LayoutVersion":"superbench.payload-layout.v1"`, `"LayoutVersion":"superbench.payload-layout.v1","Version":"superbench.service-identity.v1"`),
		"nested reordered":   changed(`"DaemonUID":1001,"DaemonGID":1003`, `"DaemonGID":1003,"DaemonUID":1001`),
		"nested duplicate":   changed(`"PayloadUID":1002`, `"PayloadUID":1002,"PayloadUID":1002`),
		"wrong UID type":     changed(`"DaemonUID":1001`, `"DaemonUID":"1001"`),
		"wrong policy type":  changed(`"Policy":`+string(policyRaw), `"Policy":[]`),
		"wrong version type": changed(`"Version":"superbench.service-identity.v1"`, `"Version":1`),
		"version":            changed(Version, "superbench.service-identity.v2"),
		"layout":             changed(payloadpolicy.LayoutVersion, "superbench.payload-layout.v2"),
		"digest mismatch":    changed(r.PolicySHA256, strings.Repeat("0", 64)),
		"uppercase digest":   changed(r.PolicySHA256, strings.ToUpper(r.PolicySHA256)),
		"short digest":       changed(r.PolicySHA256, r.PolicySHA256[:63]),
		"changed profile":    changed(`"DataRoot":"/data"`, `"DataRoot":"/other"`),
		"invalid policy":     changed(`"PayloadUID":1002`, `"PayloadUID":1001`),
		"invalid UTF8":       append(append([]byte(nil), raw...), 0xff),
		"oversize":           append(append([]byte(nil), raw...), bytes.Repeat([]byte(" "), MaxRegistry)...),
		"concatenated":       append(append([]byte(nil), raw...), raw...),
		"null":               []byte("null"), "empty": {},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseRegistry(input)
			if got != (Registry{}) || !errors.Is(err, ErrAuthority) {
				t.Fatalf("wanted zero registry and ErrAuthority: %#v %v", got, err)
			}
		})
	}
	for name, change := range map[string]func(*Registry){
		"version": func(r *Registry) { r.Version = "" }, "layout": func(r *Registry) { r.LayoutVersion = "" },
		"digest": func(r *Registry) { r.PolicySHA256 = strings.ToUpper(r.PolicySHA256) }, "policy": func(r *Registry) { r.Policy.PayloadUID = 0 },
	} {
		t.Run("bytes "+name, func(t *testing.T) {
			candidate := r
			change(&candidate)
			got, err := candidate.Bytes()
			if got != nil || !errors.Is(err, ErrAuthority) {
				t.Fatalf("invalid registry encoded: %q %v", got, err)
			}
		})
	}
}

func TestRegistryAccommodatesMaximumValidEscapedPolicy(t *testing.T) {
	p := payloadpolicy.Policy{DaemonUID: 1001, DaemonGID: 1003, PayloadUID: 1002, PayloadGID: 1004, DataRoot: "/d" + strings.Repeat("a", 4094), StateRoot: "/s" + strings.Repeat("a", 4094), CgroupRoot: "/c" + strings.Repeat("a", 4094)}
	plain, err := p.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	// Each '<' is one path byte and six canonical JSON bytes. Fill the existing
	// policy budget to within four bytes, without exceeding any4096-byte path.
	escapes := (payloadpolicy.MaxPolicy - len(plain)) / 5
	paths := []*string{&p.DataRoot, &p.StateRoot, &p.CgroupRoot}
	for _, path := range paths {
		count := min(escapes, 4094)
		*path = (*path)[:2] + strings.Repeat("<", count) + strings.Repeat("a", 4094-count)
		escapes -= count
	}
	if escapes != 0 {
		t.Fatal("fixture cannot fill policy budget")
	}
	policy, err := p.Bytes()
	if err != nil || payloadpolicy.MaxPolicy-len(policy) >= 5 || !bytes.Contains(policy, []byte(`\u003c`)) {
		t.Fatalf("maximum escaped policy invalid: bytes=%d %v", len(policy), err)
	}
	for _, path := range []string{p.DataRoot, p.StateRoot, p.CgroupRoot} {
		if len(path) != 4096 {
			t.Fatal("fixture path length changed")
		}
	}
	r := codecRegistry(p)
	raw, err := r.Bytes()
	if err != nil || len(raw) > MaxRegistry {
		t.Fatalf("valid policy exceeds registry envelope: %d %v", len(raw), err)
	}
	got, err := ParseRegistry(raw)
	if err != nil || got != r {
		t.Fatalf("maximum registry roundtrip: %v", err)
	}
}

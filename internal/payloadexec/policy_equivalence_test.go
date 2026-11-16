package payloadexec

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

// This fixed golden characterizes the existing helper policy before its pure
// codec is extracted. It is independent of either implementation's marshaler.
func TestPolicyCanonicalBytesAndRefusalsRemainStable(t *testing.T) {
	p := Policy{DaemonUID: 1001, DaemonGID: 1003, PayloadUID: 1002, PayloadGID: 1004, DataRoot: "/data", StateRoot: "/state", CgroupRoot: "/cgroup"}
	const golden = `{"DaemonUID":1001,"DaemonGID":1003,"PayloadUID":1002,"PayloadGID":1004,"DataRoot":"/data","StateRoot":"/state","CgroupRoot":"/cgroup"}`
	raw, err := p.Bytes()
	if err != nil || string(raw) != golden {
		t.Fatalf("canonical bytes changed: %q %v", raw, err)
	}
	for _, accepted := range [][]byte{raw, []byte(" \n" + golden + "\t"), bytes.ReplaceAll(raw, []byte(","), []byte(",\n "))} {
		got, err := ParsePolicy(accepted)
		if err != nil || got != p {
			t.Fatalf("accepted policy changed: %#v %v", got, err)
		}
	}
	for _, refused := range [][]byte{
		[]byte(`null`), []byte(`{}`), []byte(`[]`), []byte(strings.Replace(golden, `"DaemonUID":1001,"DaemonGID":1003`, `"DaemonGID":1003,"DaemonUID":1001`, 1)),
		[]byte(strings.Replace(golden, `"DaemonUID":1001`, `"DaemonUID":1001,"DaemonUID":1001`, 1)),
		[]byte(strings.Replace(golden, `"DaemonUID":1001`, `"DaemonUID":1.001e3`, 1)),
		[]byte(strings.Replace(golden, `"DaemonUID":1001`, `"DaemonUID":"1001"`, 1)),
		[]byte(strings.Replace(golden, `"DataRoot":"/data"`, `"DataRoot":"relative"`, 1)),
		append(append([]byte(nil), raw...), raw...), append(append([]byte(nil), raw...), 0xff), bytes.Repeat([]byte(" "), MaxPolicy+1),
	} {
		got, err := ParsePolicy(refused)
		if got != (Policy{}) || !errors.Is(err, ErrPolicy) || err.Error() != "invalid managed payload policy" {
			t.Fatalf("refusal changed: %#v %v", got, err)
		}
	}
	for _, path := range []string{"/", "relative", "/data/", "/data/../other", "/data\x00", "/data\xff", "/" + strings.Repeat("a", 4096)} {
		candidate := p
		candidate.DataRoot = path
		if raw, err := candidate.Bytes(); raw != nil || !errors.Is(err, ErrPolicy) {
			t.Fatalf("path refusal changed for %q: %q %v", path, raw, err)
		}
	}
	for _, id := range []uint32{0, ^uint32(0)} {
		candidate := p
		candidate.PayloadUID = id
		if err := candidate.Validate(); !errors.Is(err, ErrPolicy) {
			t.Fatalf("UID refusal changed: %v", err)
		}
	}
	for _, path := range []string{"/state", "/state/nested"} {
		candidate := p
		candidate.DataRoot = path
		if err := candidate.Validate(); !errors.Is(err, ErrPolicy) {
			t.Fatalf("overlapping roots accepted: %q %v", path, err)
		}
	}
	candidate := p
	candidate.DataRoot = "/" + strings.Repeat("a", 4095)
	if err := candidate.Validate(); err != nil {
		t.Fatalf("4096-byte normalized path stopped being valid: %v", err)
	}
	if MaxPolicy != 65536 || FixedPolicy != "/etc/superbench/payload-policy.json" || LayoutVersion != "superbench.payload-layout.v1" {
		t.Fatal("fixed policy/layout constants changed")
	}
}

func TestExtractedPolicyCompatibilityEntryPoints(t *testing.T) {
	// Assignment requires a true alias rather than a second lookalike policy type.
	var p Policy = payloadpolicy.Policy{DaemonUID: 1001, DaemonGID: 1003, PayloadUID: 1002, PayloadGID: 1004, DataRoot: "/data/<é>", StateRoot: "/state", CgroupRoot: "/cgroup"}
	raw, err := p.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`/data/\u003cé\u003e`)) {
		t.Fatalf("JSON escaping changed: %q", raw)
	}
	for _, input := range [][]byte{raw, append([]byte("\n"), raw...), []byte(`null`), []byte(`{"DaemonUID":0}`), []byte{0xff}} {
		compatible, oldErr := ParsePolicy(input)
		extracted, newErr := payloadpolicy.ParsePolicy(input)
		if compatible != extracted || oldErr != newErr {
			t.Fatalf("entry points differ: %#v %v / %#v %v", compatible, oldErr, extracted, newErr)
		}
	}
	if ErrPolicy != payloadpolicy.ErrPolicy || MaxPolicy != payloadpolicy.MaxPolicy || FixedPolicy != payloadpolicy.FixedPolicy || LayoutVersion != payloadpolicy.LayoutVersion {
		t.Fatal("compatibility identity changed")
	}
	for _, path := range []string{"/data", "/", "relative", "/data/", "/data\x00", "/data\xff"} {
		if directory(path) != payloadpolicy.Directory(path) {
			t.Fatalf("directory compatibility changed: %q", path)
		}
	}
}

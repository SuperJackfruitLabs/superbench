package payloadexec

import (
	"bytes"
	"errors"
	"testing"
)

func TestFixedPolicyCannotBeExtendedOrRedirectCredentials(t *testing.T) {
	p := Policy{DaemonUID: 1001, DaemonGID: 1001, PayloadUID: 1002, PayloadGID: 1002, DataRoot: "/srv/superbench-data", StateRoot: "/var/lib/superbench", CgroupRoot: "/sys/fs/cgroup/superbench"}
	raw, err := p.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ParsePolicy(raw); err != nil || got != p {
		t.Fatalf("canonical policy: %#v %v", got, err)
	}
	for _, invalid := range [][]byte{
		append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"PayloadUID":0}`)...),
		append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"command":"sh"}`)...),
		bytes.Replace(raw, []byte(`1002`), []byte(`"1002"`), 1),
		append(append([]byte(nil), raw...), raw...),
		append(append([]byte(nil), raw...), bytes.Repeat([]byte(" "), MaxPolicy)...),
	} {
		if _, err := ParsePolicy(invalid); !errors.Is(err, ErrPolicy) {
			t.Fatalf("invalid policy accepted: %v", err)
		}
	}
}

package serviceidentity

import (
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

func registryFixture() Registry {
	return codecRegistry(payloadpolicy.Policy{DaemonUID: 1001, DaemonGID: 1003, PayloadUID: 1002, PayloadGID: 1004, DataRoot: "/data", StateRoot: "/state", CgroupRoot: "/cgroup"})
}

func TestZeroAuthorityNeverResolvesImplicitly(t *testing.T) {
	var authority Authority
	if err := authority.Recheck(); !errors.Is(err, ErrAuthority) {
		t.Fatalf("zero authority recheck: %v", err)
	}
	if p, err := authority.Policy(); !errors.Is(err, ErrAuthority) || p.DaemonUID != 0 {
		t.Fatalf("zero authority policy: %+v %v", p, err)
	}
}

func TestRegistryDerivedFromOnePolicy(t *testing.T) {
	p := registryFixture().Policy
	r, err := ForPolicy(p)
	if err != nil || r != registryFixture() {
		t.Fatalf("derived registry: %+v %v", r, err)
	}
	p.PayloadUID = p.DaemonUID
	if got, err := ForPolicy(p); !errors.Is(err, ErrAuthority) || got != (Registry{}) {
		t.Fatalf("invalid policy derived authority metadata: %+v %v", got, err)
	}
}

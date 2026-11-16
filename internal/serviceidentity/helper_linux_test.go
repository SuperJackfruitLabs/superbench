//go:build linux

package serviceidentity

import (
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

func TestRootHelperAndDaemonIdentityAreSeparate(t *testing.T) {
	if err := helperCredentials(0, 0); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][2]int{{1001, 1001}, {0, 1001}, {1001, 0}, {1002, 1002}, {-1, 0}} {
		if err := helperCredentials(ids[0], ids[1]); !errors.Is(err, ErrAuthority) {
			t.Fatalf("helper identities %v: %v", ids, err)
		}
	}
	p := payloadpolicy.Policy{DaemonUID: 1001, DaemonGID: 1001, PayloadUID: 1002, PayloadGID: 1002, DataRoot: "/data", StateRoot: "/state", CgroupRoot: "/cgroup"}
	if err := serviceCredentials(p, 0, 0, 0, 0); !errors.Is(err, ErrAuthority) {
		t.Fatalf("root became daemon authority: %v", err)
	}
}

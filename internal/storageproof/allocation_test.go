package storageproof

import (
	"encoding/json"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

func TestManagedUnallocatedObservationCannotBecomeAllocatedIdentityOrRetiredAuthority(t *testing.T) {
	o := AllocationObservation{Version: AllocationVersion, Root: "/fixed/data", BenchKey: "card", Device: 1, Inode: 2, Mount: "actual", Managed: true, FilesystemUUID: "11111111-1111-1111-1111-111111111111", PolicySHA256: strings.Repeat("1", 64), LedgerSHA256: strings.Repeat("2", 64), AnchorSHA256: strings.Repeat("3", 64), ConfigSHA256: strings.Repeat("4", 64), Assignment: quotahelper.Assignment{Path: "/fixed/data/benches/card"}}
	raw, err := o.Bytes()
	if err != nil || o.Allocated() {
		t.Fatal(o, err)
	}
	if got, err := ParseAllocationObservation(raw); err != nil || got != o {
		t.Fatal(got, err)
	}
	identity := Identity(o)
	identity.Version = Version
	if err := identity.Validate(); err == nil {
		t.Fatal("unallocated observation became allocated storage identity")
	}
	for _, kind := range []string{"retired", "missing-inodes", "foreign", "policy", "version", "duplicate", "unknown", "newline"} {
		t.Run(kind, func(t *testing.T) {
			bad := o
			switch kind {
			case "retired":
				bad.Assignment.ProjectID = 10000
			case "missing-inodes":
				bad.Assignment.ProjectID = 10000
				bad.Assignment.Bytes = 8192
			case "foreign":
				bad.Assignment.Path += "-other"
			case "policy":
				bad.PolicySHA256 = ""
			case "version":
				bad.Version = Version
			}
			raw, _ := json.Marshal(bad)
			switch kind {
			case "duplicate":
				raw = append(raw[:len(raw)-1], []byte(`,"Managed":true}`)...)
			case "unknown":
				raw = append(raw[:len(raw)-1], []byte(`,"unknown":1}`)...)
			case "newline":
				raw = append(raw, '\n')
			}
			if _, err := ParseAllocationObservation(raw); err == nil {
				t.Fatal("invalid allocation authority accepted")
			}
		})
	}
	o.Assignment.ProjectID, o.Assignment.Bytes, o.Assignment.Inodes = 10000, 8192, 128
	if _, err := o.Bytes(); err != nil || !o.Allocated() {
		t.Fatal("assigned observation refused", err)
	}
}

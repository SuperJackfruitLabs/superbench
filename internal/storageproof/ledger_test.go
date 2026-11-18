package storageproof

import (
	"encoding/json"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
)

func TestLegacyReleaseRecordsPreserveLegacyAndRefuseFreshOrForeignAssignments(t *testing.T) {
	binding := allocationledger.Binding{OwnerUID: 1001, OwnerGID: 1001, BenchBytes: 1024, StoreBytes: 1024, MirrorBytes: 1024, ReserveBytes: 1024, BenchInodes: 1, StoreInodes: 1, MirrorInodes: 1, ReserveInodes: 1}
	for _, kind := range []string{"legacy0", "bound2", "bound3", "unrelated-fresh", "active-fresh", "retired-fresh", "unknown", "wrong-project", "invalid-row", "unknown-field"} {
		t.Run(kind, func(t *testing.T) {
			l := allocationledger.Ledger{FormatVersion: 3, FilesystemUUID: "11111111-1111-1111-1111-111111111111", Binding: &binding, Root: "/bench", Next: 10001, Assignments: map[string]quotapolicy.Assignment{"bench:card": {Path: "/bench/benches/card", ProjectID: 10000, Bytes: 1024, Inodes: 1}}}
			key, project := "card", uint32(10000)
			switch kind {
			case "legacy0":
				l.FormatVersion, l.VolumeID, l.FilesystemUUID, l.Binding = 0, l.FilesystemUUID, "", nil
			case "bound2":
				l.FormatVersion = 2
			case "unrelated-fresh":
				l.Next++
				l.Assignments["bench:other"] = quotapolicy.Assignment{Path: "/bench/benches/other", ProjectID: 10001, Bytes: 1024, Inodes: 1}
				l.Fresh = map[string]uint32{"bench:other": 10001}
			case "active-fresh", "retired-fresh":
				l.Fresh = map[string]uint32{"bench:card": 10000}
				if kind == "retired-fresh" {
					a := l.Assignments["bench:card"]
					a.Bytes, a.Inodes = 0, 0
					l.Assignments["bench:card"] = a
				}
			case "unknown":
				key = "unknown"
			case "wrong-project":
				project++
			case "invalid-row":
				a := l.Assignments["bench:card"]
				a.Path += "-foreign"
				l.Assignments["bench:card"] = a
			}
			raw, err := json.Marshal(l)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "unknown-field" {
				raw = append(raw[:len(raw)-1], []byte(`,"Unknown":10001}`)...)
			}
			fresh, err := LegacyReleaseLedger(l.Root, binding, false, key, project, raw)
			switch kind {
			case "active-fresh", "retired-fresh":
				if err != nil || !fresh {
					t.Fatalf("Fresh refusal not selected: fresh=%v err=%v", fresh, err)
				}
			case "unknown", "wrong-project", "invalid-row", "unknown-field":
				if err == nil || fresh {
					t.Fatalf("foreign release accepted: fresh=%v err=%v", fresh, err)
				}
			default:
				if err != nil || fresh {
					t.Fatalf("ordinary allocation refused: fresh=%v err=%v", fresh, err)
				}
			}
		})
	}
}

// The storage authority every retained intent binds is the same before and
// after a key's next generation, so an earlier generation's reclaim and the
// next one's bootstrap name one authority.
func TestOperationAuthoritySurvivesAKeysNextGeneration(t *testing.T) {
	binding := allocationledger.Binding{OwnerUID: 1001, OwnerGID: 1001, BenchBytes: 1024, StoreBytes: 1024, MirrorBytes: 1024, ReserveBytes: 1024, BenchInodes: 1, StoreInodes: 1, MirrorInodes: 1, ReserveInodes: 1}
	l := allocationledger.Ledger{FormatVersion: 3, FilesystemUUID: "11111111-1111-1111-1111-111111111111", Binding: &binding, Root: "/bench", Next: 10001,
		Fresh: map[string]uint32{"bench:card": 10000}, Assignments: map[string]quotapolicy.Assignment{"bench:card": {Path: "/bench/benches/card", ProjectID: 10000}}}
	anchor, _ := json.Marshal(map[string]string{"Root": "/bench", "Ledger": "/ledger"})
	before, _ := json.Marshal(l)
	if err := l.Succeed("bench:card", "/bench/benches/card", 10000); err != nil {
		t.Fatal(err)
	}
	l.Assignments["bench:card"] = quotapolicy.Assignment{Path: "/bench/benches/card", ProjectID: 10001, Bytes: 1024, Inodes: 1}
	l.Fresh["bench:card"], l.Next = 10001, 10002
	after, _ := json.Marshal(l)
	_, a, err := OperationAuthorityRecords("/bench", "/ledger", binding, false, before, anchor)
	_, b, err2 := OperationAuthorityRecords("/bench", "/ledger", binding, false, after, anchor)
	if err != nil || err2 != nil || a != b {
		t.Fatalf("authority changed across a generation: %v %v", err, err2)
	}
}

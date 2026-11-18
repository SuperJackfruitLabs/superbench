package freshretirement

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

func recordsFixture() (hoststorage.Policy, allocationledger.Ledger, Approval) {
	p := hoststorage.Policy{OperationJournal: "indexed-v2", Root: "/srv/data", Ledger: "/var/lib/superbench/quota", OwnerUID: 1001, OwnerGID: 1001,
		BenchBytes: 32 << 20, StoreBytes: 16 << 20, MirrorBytes: 16 << 20, ReserveBytes: 16 << 20,
		BenchInodes: 4096, StoreInodes: 4096, MirrorInodes: 4096, ReserveInodes: 4096}
	binding := p.AllocationBinding()
	a := hoststorage.Assignment{Path: "/srv/data/benches/card", ProjectID: 10000, Bytes: 16 << 20, Inodes: 4096}
	l := allocationledger.Ledger{FormatVersion: 3, FilesystemUUID: "11111111-2222-3333-4444-555555555555", Binding: &binding, Root: p.Root, Next: 10002,
		Fresh: map[string]uint32{"bench:card": a.ProjectID}, Assignments: map[string]hoststorage.Assignment{"bench:card": a,
			"bench:other": {Path: "/srv/data/benches/other", ProjectID: 10001, Bytes: 8 << 20, Inodes: 4096}}}
	return p, l, Approval{Version: Version, Key: "card", Authority: strings.Repeat("a", 64), Assignment: a, Parent: Parent{Device: 8, Inode: 2, Mode: uint32(os.ModeDir | 0711)}}
}

func encoded(t *testing.T, l allocationledger.Ledger) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSelectedFreshReservationRequiresExactMarkerAndOriginalAssignment(t *testing.T) {
	p, l, a := recordsFixture()
	if err := a.validate(p); err != nil {
		t.Fatal("valid approval refused", err)
	}
	raw := encoded(t, l)
	gotLedger, got, retired, err := selected(p, a.Key, raw, nil)
	if err != nil || got != a.Assignment || retired || !reflect.DeepEqual(gotLedger, l) {
		t.Fatalf("valid observed selection: %+v %v %v", got, retired, err)
	}
	for _, kind := range []string{"marker-missing", "marker-foreign", "legacy", "unbound", "foreign-policy", "foreign-row", "foreign-unrelated-row", "unknown", "retired-without-original"} {
		t.Run(kind, func(t *testing.T) {
			p, l, a := recordsFixture()
			switch kind {
			case "marker-missing":
				delete(l.Fresh, "bench:card")
			case "marker-foreign":
				l.Fresh["bench:card"]++
			case "legacy":
				l.FormatVersion, l.Fresh = 2, nil
			case "unbound":
				l.Binding = nil
			case "foreign-policy":
				l.Binding.OwnerUID++
			case "foreign-row":
				v := l.Assignments["bench:card"]
				v.Path = "/srv/data/benches/other"
				l.Assignments["bench:card"] = v
			case "foreign-unrelated-row":
				v := l.Assignments["bench:other"]
				v.ProjectID = a.Assignment.ProjectID
				l.Assignments["bench:other"] = v
			case "unknown":
				a.Key = "missing"
			case "retired-without-original":
				v := a.Assignment
				v.Bytes, v.Inodes = 0, 0
				l.Assignments["bench:card"] = v
			}
			if _, got, _, err := selected(p, a.Key, encoded(t, l), nil); !errors.Is(err, ErrRetirement) || got != (hoststorage.Assignment{}) {
				t.Fatalf("foreign selection admitted: %+v %v", got, err)
			}
		})
	}
}

func TestRetiredFreshRowRequiresRetainedOriginalApproval(t *testing.T) {
	p, l, a := recordsFixture()
	tombstone := a.Assignment
	tombstone.Bytes, tombstone.Inodes = 0, 0
	l.Assignments["bench:card"] = tombstone
	raw := encoded(t, l)
	_, original, retired, err := selected(p, a.Key, raw, &a.Assignment)
	if err != nil || original != a.Assignment || !retired {
		t.Fatalf("lost-reply retry refused: %+v %v %v", original, retired, err)
	}
	for _, change := range []func(*hoststorage.Assignment){
		func(v *hoststorage.Assignment) { v.ProjectID++ },
		func(v *hoststorage.Assignment) { v.Path += "-foreign" },
	} {
		foreign := a.Assignment
		change(&foreign)
		if _, _, _, err := selected(p, a.Key, raw, &foreign); !errors.Is(err, ErrRetirement) {
			t.Fatalf("foreign original adopted tombstone: %v", err)
		}
	}
	// Active rows must match all caps, unlike a tombstone whose original caps
	// can only come from the separately retained reviewed approval.
	l.Assignments["bench:card"] = a.Assignment
	foreign := a.Assignment
	foreign.Bytes /= 2
	if _, _, _, err := selected(p, a.Key, encoded(t, l), &foreign); !errors.Is(err, ErrRetirement) {
		t.Fatalf("changed active byte cap accepted: %v", err)
	}
}

func TestApprovalRefusesForeignPolicyPathsCapsAndParent(t *testing.T) {
	for name, change := range map[string]func(*Approval){
		"version": func(a *Approval) { a.Version = "foreign" }, "key": func(a *Approval) { a.Key = "../escape" },
		"authority": func(a *Approval) { a.Authority = "invented" }, "path": func(a *Approval) { a.Assignment.Path += "-other" },
		"shared-project": func(a *Approval) { a.Assignment.ProjectID = 100 }, "zero-bytes": func(a *Approval) { a.Assignment.Bytes = 0 },
		"fractional-kib": func(a *Approval) { a.Assignment.Bytes++ }, "over-cap": func(a *Approval) { a.Assignment.Bytes = 64 << 20 },
		"inodes": func(a *Approval) { a.Assignment.Inodes++ }, "device": func(a *Approval) { a.Parent.Device = 0 },
		"inode": func(a *Approval) { a.Parent.Inode = 0 }, "owner": func(a *Approval) { a.Parent.UID = 1001 },
		"group-writable": func(a *Approval) { a.Parent.Mode |= 0020 }, "world-writable": func(a *Approval) { a.Parent.Mode |= 0002 },
		"symlink": func(a *Approval) { a.Parent.Mode = uint32(os.ModeSymlink | 0700) },
	} {
		t.Run(name, func(t *testing.T) {
			p, _, a := recordsFixture()
			change(&a)
			if err := a.validate(p); !errors.Is(err, ErrRetirement) {
				t.Fatalf("invalid approval admitted: %v", err)
			}
		})
	}
}

func TestSelectedRefusesAmbiguousOrNoncanonicalLedger(t *testing.T) {
	p, l, a := recordsFixture()
	raw := encoded(t, l)
	compact, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{
		compact, append(append([]byte{}, raw...), '\n'),
		[]byte(strings.Replace(string(raw), `"Next": 10002`, `"Next": 10001, "Next": 10002`, 1)),
		[]byte(strings.Replace(string(raw), `"Next": 10002`, `"Unknown": 1, "Next": 10002`, 1)),
		append(append([]byte{}, raw...), []byte(` {}`)...),
	} {
		if _, _, _, err := selected(p, a.Key, bad, nil); !errors.Is(err, ErrRetirement) {
			t.Fatalf("ambiguous record admitted: %v", err)
		}
	}
}

// A key on its second generation selects that generation's project; the
// earlier one is evidence in Retired, never a selectable row.
func TestSelectedTakesTheCurrentGenerationOfAReclaimedKey(t *testing.T) {
	p, l, a := recordsFixture()
	a.Assignment.ProjectID, l.Next = 10002, 10003
	l.FormatVersion, l.Retired = allocationledger.FormatGenerations, []allocationledger.RetiredBench{{Name: "bench:card", ProjectID: 10000}}
	l.Assignments["bench:card"], l.Fresh["bench:card"] = a.Assignment, a.Assignment.ProjectID
	if _, got, retired, err := selected(p, a.Key, encoded(t, l), nil); err != nil || got != a.Assignment || retired {
		t.Fatalf("second generation: %+v %v %v", got, retired, err)
	}
	l.FormatVersion = 5
	if _, _, _, err := selected(p, a.Key, encoded(t, l), nil); err == nil {
		t.Fatal("an unknown format selected")
	}
}

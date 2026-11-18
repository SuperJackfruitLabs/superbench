package allocationledger

import (
	"maps"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
)

var binding = Binding{OwnerUID: 1001, OwnerGID: 1001, BenchBytes: 8 << 20, StoreBytes: 16 << 20, MirrorBytes: 16 << 20, ReserveBytes: 64 << 20, BenchInodes: 128, StoreInodes: 256, MirrorInodes: 512, ReserveInodes: 256}

// first is a format-3 ledger whose Fresh key "card" was reclaimed: its
// project 10000 is a tombstone.
func first() Ledger {
	b := binding
	return Ledger{FormatVersion: 3, FilesystemUUID: "11111111-1111-1111-1111-111111111111", Binding: &b, Root: "/bench", Next: 10001,
		Fresh: map[string]uint32{"bench:card": 10000}, Assignments: map[string]quotapolicy.Assignment{"bench:card": {Path: "/bench/benches/card", ProjectID: 10000}}}
}

// second is first after the key's second generation took project 10001.
func second(t *testing.T) Ledger {
	t.Helper()
	l := first()
	if err := l.Succeed("bench:card", "/bench/benches/card", 10000); err != nil {
		t.Fatal(err)
	}
	l.Assignments["bench:card"] = quotapolicy.Assignment{Path: "/bench/benches/card", ProjectID: 10001, Bytes: binding.BenchBytes, Inodes: binding.BenchInodes}
	l.Fresh["bench:card"], l.Next = 10001, 10002
	return l
}

func TestSucceedRetiresOnlyTheExactReclaimedTombstone(t *testing.T) {
	l := first()
	before := first()
	if err := l.Succeed("bench:card", "/bench/benches/card", 10000); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Assignments["bench:card"]; ok || l.Fresh["bench:card"] != 0 || l.FormatVersion != FormatGenerations || !reflect.DeepEqual(l.Retired, []RetiredBench{{"bench:card", 10000}}) {
		t.Fatalf("succeeded ledger %+v", l)
	}
	if !maps.Equal(before.Assignments, first().Assignments) {
		t.Fatal("the caller's rows changed")
	}
	live := first()
	live.Assignments["bench:card"] = quotapolicy.Assignment{Path: "/bench/benches/card", ProjectID: 10000, Bytes: 1024, Inodes: 1}
	unmarked := first()
	unmarked.Fresh = nil
	for name, c := range map[string]struct {
		l        Ledger
		path     string
		previous uint32
	}{
		"not reclaimed":   {live, "/bench/benches/card", 10000},
		"not fresh":       {unmarked, "/bench/benches/card", 10000},
		"another project": {first(), "/bench/benches/card", 10001},
		"another path":    {first(), "/bench/benches/other", 10000},
		"shared range":    {Ledger{Assignments: map[string]quotapolicy.Assignment{"bench:card": {Path: "/bench/benches/card", ProjectID: 101}}, Fresh: map[string]uint32{"bench:card": 101}}, "/bench/benches/card", 101},
		"unknown key":     {first(), "/bench/benches/card", 10000},
	} {
		key := "bench:card"
		if name == "unknown key" {
			key = "bench:other"
		}
		if err := c.l.Succeed(key, c.path, c.previous); err == nil {
			t.Errorf("%s: succeeded", name)
		}
	}
}

// The second generation validates, and the earlier project stays retired.
func TestEarlierGenerationsStayRetiredEvidence(t *testing.T) {
	if err := second(t).Validate("/bench", binding, false); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Ledger){
		"format 3": func(l *Ledger) { l.FormatVersion = 3 },
		"retired reused": func(l *Ledger) {
			l.Assignments["bench:other"] = quotapolicy.Assignment{Path: "/bench/benches/other", ProjectID: 10000}
		},
		"retired twice":       func(l *Ledger) { l.Retired = append(l.Retired, l.Retired[0]) },
		"no later generation": func(l *Ledger) { delete(l.Assignments, "bench:card"); delete(l.Fresh, "bench:card") },
		"later is older":      func(l *Ledger) { l.Next, l.Retired[0].ProjectID = 10004, 10003 },
		"later not fresh":     func(l *Ledger) { delete(l.Fresh, "bench:card") },
		"below fresh range":   func(l *Ledger) { l.Retired[0].ProjectID = 101 },
		"not a bench":         func(l *Ledger) { l.Retired[0].Name = "store" },
	} {
		l := second(t)
		change(&l)
		if err := l.Validate("/bench", binding, false); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	l := second(t)
	l.Retired = nil
	if err := l.Validate("/bench", binding, false); err != nil {
		t.Fatal("format 4 without earlier generations refused", err)
	}
	for v, bound := range map[int]bool{0: false, 2: true, 3: true, 4: true, 5: false} {
		if (Ledger{FormatVersion: v}).Bound() != bound {
			t.Errorf("format %d bound = %v", v, !bound)
		}
	}
	if err := (Ledger{FormatVersion: 5}).Header(binding, false); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatal("format 5 accepted", err)
	}
}

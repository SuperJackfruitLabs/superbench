package freshretirement

import (
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

// A key root never allocated has no row and no Fresh marker; after a
// predecessor, its row and marker are still exactly that tombstone. Any
// allocation of the key's own, live or retired, is not absence.
func TestUnallocatedRequiresNothingOfTheKeysOwn(t *testing.T) {
	p, l, _ := recordsFixture()
	delete(l.Assignments, "bench:card")
	delete(l.Fresh, "bench:card")
	if _, _, _, err := unallocated(p, "card", encoded(t, l), nil); err != nil {
		t.Fatal("a key never allocated:", err)
	}
	// The live allocation a completed reservation leaves, and the same row
	// without its marker, as a legacy allocation of the key would be.
	for name, marked := range map[string]bool{"a Fresh allocation": true, "a legacy allocation": false} {
		_, l, _ = recordsFixture()
		l.Assignments["bench:card"] = hoststorage.Assignment{Path: "/srv/data/benches/card", ProjectID: 10000, Bytes: 16 << 20, Inodes: 4096}
		if !marked {
			delete(l.Fresh, "bench:card")
		}
		if _, _, _, err := selected(p, "card", encoded(t, l), nil); marked && err != nil {
			t.Fatalf("%s: the case is not a valid ledger: %v", name, err)
		}
		if _, _, _, err := unallocated(p, "card", encoded(t, l), nil); !errors.Is(err, ErrRetirement) {
			t.Errorf("%s: accepted as never allocated: %v", name, err)
		}
	}
	// A later generation: the predecessor's tombstone stands in the ledger.
	tombstone := hoststorage.Assignment{Path: "/srv/data/benches/card", ProjectID: 10000}
	_, l, _ = recordsFixture()
	l.Assignments["bench:card"] = tombstone
	if _, got, _, err := unallocated(p, "card", encoded(t, l), &tombstone); err != nil || got != tombstone {
		t.Fatalf("a later generation never allocated: %+v %v", got, err)
	}
	for name, change := range map[string]func(){
		"its own project": func() {
			// The ledger a later generation's completed reservation leaves.
			l.FormatVersion, l.Next = allocationledger.FormatGenerations, 10003
			l.Retired = []allocationledger.RetiredBench{{Name: "bench:card", ProjectID: 10000}}
			l.Assignments["bench:card"] = hoststorage.Assignment{Path: "/srv/data/benches/card", ProjectID: 10002, Bytes: 16 << 20, Inodes: 4096}
			l.Fresh["bench:card"] = 10002
			if _, _, _, err := selected(p, "card", encoded(t, l), nil); err != nil {
				t.Fatalf("its own project: the case is not a valid ledger: %v", err)
			}
		},
		"a live predecessor": func() {
			l.Assignments["bench:card"] = hoststorage.Assignment{Path: tombstone.Path, ProjectID: 10000, Bytes: 16 << 20, Inodes: 4096}
		},
		"no marker": func() { delete(l.Fresh, "bench:card") },
		"no row":    func() { delete(l.Assignments, "bench:card"); delete(l.Fresh, "bench:card") },
	} {
		_, l, _ = recordsFixture()
		l.Assignments["bench:card"] = tombstone
		change()
		if _, _, _, err := unallocated(p, "card", encoded(t, l), &tombstone); !errors.Is(err, ErrRetirement) {
			t.Errorf("%s: accepted as never allocated: %v", name, err)
		}
	}
	other := hoststorage.Assignment{Path: "/srv/data/benches/other", ProjectID: 10000}
	_, l, _ = recordsFixture()
	l.Assignments["bench:card"] = tombstone
	if _, _, _, err := unallocated(p, "card", encoded(t, l), &other); !errors.Is(err, ErrRetirement) {
		t.Fatal("another key's predecessor accepted")
	}
	t.Log("CAPACITY_REFUSED_LEDGER_NEVER_ALLOCATED_PASS")
}

//go:build linux

package hoststorage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Exercise the actual post-lock Apply continuation without a privileged mount:
// a missing parent fails mkdir AFTER the durable reservation, BEFORE xfs tools.
func TestFreshReservationPersistsBeforeEffectsAndBlocksEveryRetry(t *testing.T) {
	p := testPolicy()
	p.Root = filepath.Join(t.TempDir(), "absent")
	p.Ledger = t.TempDir()
	l := ledger{Root: p.Root, VolumeID: "11111111-1111-1111-1111-111111111111", Next: 10000, Assignments: map[string]Assignment{}}
	p.bindLedger(&l)
	name, key := "bench:card", "card"
	path := filepath.Join(p.Root, "benches", key)
	calls, reservations := 0, 0
	reservationError := errors.New("provenance publication failed")
	init := func(Assignment) error { calls++; return nil }
	fs := syscall.Statfs_t{Files: 1 << 30, Blocks: 1 << 30, Bsize: 4096}
	apply := func(rows ledger, callback func(Assignment) error) error {
		var reserve func() error
		if callback != nil {
			reserve = func() error {
				reservations++
				if _, err := os.Lstat(filepath.Join(p.Ledger, "assignments.json")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("assignment preceded provenance", err)
				}
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("filesystem effect preceded provenance", err)
				}
				return reservationError
			}
		}
		_, err := p.applyLedger(rows, "bench", key, name, path, 0, p.BenchBytes, p.BenchInodes, 0, fs, reserve, callback)
		return err
	}
	if err := apply(l, init); !errors.Is(err, reservationError) || len(l.Assignments) != 0 || calls != 0 {
		t.Fatalf("failed provenance publication allocated: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(p.Ledger, "assignments.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed provenance saved assignment", err)
	}
	reservationError = nil
	if err := apply(l, init); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected pre-xfs mkdir failure, got %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(p.Ledger, "assignments.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved ledger
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.FormatVersion != 3 || saved.Fresh[name] != 10000 || saved.Assignments[name].ProjectID != 10000 || saved.Next != 10001 {
		t.Fatalf("fresh marker not atomic with ID: %+v", saved)
	}
	if err = p.checkLedger(saved); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Join(p.Ledger, "payload-bootstrap")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected intent: %v", err)
	}
	for _, c := range []struct {
		name     string
		callback func(Assignment) error
		want     error
	}{{"fresh-known", init, ErrFreshAllocation}, {"legacy-marker", nil, ErrRegisteredAllocation}} {
		t.Run(c.name, func(t *testing.T) {
			if err := apply(saved, c.callback); !errors.Is(err, c.want) {
				t.Fatalf("retry refusal = %v, want %v", err, c.want)
			}
		})
	}
	t.Run("retained-intent-callsite", func(t *testing.T) {
		rows := saved
		rows.Fresh = nil
		dir := filepath.Join(p.Ledger, "payload-bootstrap")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "card.json"), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := apply(rows, nil); !errors.Is(err, ErrRegisteredAllocation) {
			t.Fatalf("intent callsite refusal = %v", err)
		}
	})
	after, err := os.ReadFile(filepath.Join(p.Ledger, "assignments.json"))
	if err != nil || string(after) != string(raw) || calls != 0 || reservations != 2 {
		t.Fatalf("refusal changed ledger or initialized: %v calls=%d", err, calls)
	}
	// Retirement and rebinding must keep both the marker and format-3 fence.
	a := saved.Assignments[name]
	a.Bytes, a.Inodes = 0, 0
	saved.Assignments[name] = a
	p.bindLedger(&saved)
	if saved.FormatVersion != 3 || saved.Fresh[name] != 10000 {
		t.Fatal("rebinding lost fresh exclusion")
	}
	if err = p.checkLedger(saved); err != nil {
		t.Fatal(err)
	}
}

func TestFreshLedgerRejectsUnboundForeignAndSharedMarkers(t *testing.T) {
	p, l, _ := storageRecords(t)
	l.FormatVersion = 3
	l.Fresh = map[string]uint32{"bench:source": l.Assignments["bench:source"].ProjectID}
	if err := p.checkLedger(l); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		version int
		fresh   map[string]uint32
	}{{"old-format", 2, l.Fresh}, {"foreign-id", 3, map[string]uint32{"bench:source": 10099}}, {"missing-row", 3, map[string]uint32{"bench:missing": 10000}}, {"shared", 3, map[string]uint32{"store": 100}}} {
		t.Run(c.name, func(t *testing.T) {
			rows := l
			rows.FormatVersion, rows.Fresh = c.version, c.fresh
			if err := p.checkLedger(rows); err == nil || !strings.Contains(err.Error(), "fresh") {
				t.Fatalf("invalid fresh marker refusal = %v", err)
			}
		})
	}
}

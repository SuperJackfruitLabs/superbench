//go:build linux

package hoststorage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A key's next generation replaces only its reclaimed tombstone. Refusals
// precede the reservation and save; the accepted case saves, past the first
// generation's retained intent, then fails at the absent root before effects.
func TestSuccessorReplacesOnlyTheReclaimedTombstone(t *testing.T) {
	p := testPolicy()
	p.Root, p.Ledger = filepath.Join(t.TempDir(), "absent"), t.TempDir()
	name, key := "bench:card", "card"
	path := filepath.Join(p.Root, "benches", key)
	tombstone := Assignment{Path: path, ProjectID: 10000}
	rows := func(a Assignment, fresh bool) ledger {
		l := ledger{Root: p.Root, VolumeID: "11111111-1111-1111-1111-111111111111", Next: 10001, Assignments: map[string]Assignment{name: a}}
		p.bindLedger(&l)
		if fresh {
			l.FormatVersion, l.Fresh = 3, map[string]uint32{name: a.ProjectID}
		}
		return l
	}
	fs, reserved := syscall.Statfs_t{Files: 1 << 30, Blocks: 1 << 30, Bsize: 4096}, 0
	apply := func(p Policy, l ledger, kind string, previous Assignment, initialize func(Assignment) error) error {
		_, err := p.applyLedger(l, kind, key, name, filepath.Join(p.Root, "benches", key), 0, p.BenchBytes, p.BenchInodes, 0, fs, func() error { reserved++; return nil }, initialize, &previous)
		return err
	}
	init, live := func(Assignment) error { return nil }, Assignment{Path: path, ProjectID: 10000, Bytes: p.BenchBytes, Inodes: p.BenchInodes}
	for name, c := range map[string]struct {
		l        ledger
		kind     string
		previous Assignment
		init     func(Assignment) error
	}{
		"wrong path":          {rows(tombstone, true), "bench", Assignment{Path: path + "-other", ProjectID: 10000}, init},
		"wrong project":       {rows(tombstone, true), "bench", Assignment{Path: path, ProjectID: 10001}, init},
		"live row":            {rows(live, true), "bench", tombstone, init},
		"missing Fresh mark":  {rows(tombstone, false), "bench", tombstone, init},
		"not a bench":         {rows(tombstone, true), "shared", tombstone, init},
		"no fresh initialize": {rows(tombstone, true), "bench", tombstone, nil},
	} {
		if err := apply(p, c.l, c.kind, c.previous, c.init); !errors.Is(err, ErrFreshAllocation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	present := p // the reclaimed generation's leaf must be gone, never adopted
	present.Root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(present.Root, "benches", key), 0700); err != nil {
		t.Fatal(err)
	}
	l := rows(Assignment{Path: filepath.Join(present.Root, "benches", key), ProjectID: 10000}, true)
	l.Root = present.Root
	if err := apply(present, l, "bench", l.Assignments[name], init); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("leaf present: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(p.Ledger, "assignments.json")); reserved != 0 || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused successor reserved (%d) or saved: %v", reserved, err)
	}
	if err := os.MkdirAll(filepath.Join(p.Ledger, "payload-bootstrap"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Ledger, "payload-bootstrap", key+".json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := apply(p, rows(tombstone, true), "bench", tombstone, init); !errors.Is(err, os.ErrNotExist) || reserved != 1 {
		t.Fatalf("successor: %v", err)
	}
	var saved ledger // the first generation's intent above refused only a first allocation
	raw, err := os.ReadFile(filepath.Join(p.Ledger, "assignments.json"))
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.Assignments[name].ProjectID != 10001 || saved.Fresh[name] != 10001 || len(saved.Retired) != 1 || saved.Retired[0].ProjectID != 10000 || saved.FormatVersion != 4 || p.checkLedger(saved) != nil {
		t.Fatalf("successor ledger %+v: %v", saved, err)
	}
}

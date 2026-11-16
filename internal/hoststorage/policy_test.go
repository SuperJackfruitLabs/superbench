package hoststorage

import (
	"path/filepath"
	"strings"
	"testing"
)

func testPolicy() Policy {
	return Policy{Root: "/bench", Ledger: "/var/lib/superbench-quota", OwnerUID: 1000, OwnerGID: 1000, BenchBytes: 8 << 20, StoreBytes: 16 << 20, MirrorBytes: 16 << 20, ReserveBytes: 64 << 20, BenchInodes: 128, StoreInodes: 256, MirrorInodes: 512, ReserveInodes: 256}
}
func TestPolicyRejectsUnsafePathsAndBudgets(t *testing.T) {
	for _, name := range []string{"root", "relative", "metachar", "overlap", "reverse-overlap", "owner", "fraction", "huge", "journal"} {
		t.Run(name, func(t *testing.T) {
			p := testPolicy()
			switch name {
			case "journal":
				p.OperationJournal = "unknown"
			case "root":
				p.Root = "/"
			case "relative":
				p.Root = "bench"
			case "metachar":
				p.Root = "/bench;limit"
			case "overlap":
				p.Ledger = filepath.Join(p.Root, "ledger")
			case "reverse-overlap":
				p.Root = filepath.Join(p.Ledger, "bench")
			case "owner":
				p.OwnerUID = -1
			case "fraction":
				p.BenchBytes = 1025
			case "huge":
				p.BenchBytes = 1 << 60
			}
			if p.Validate() == nil {
				t.Fatal("unsafe policy accepted")
			}
		})
	}
	if e := testPolicy().Validate(); e != nil {
		t.Fatal(e)
	}
}
func TestLedgerRejectsReassignmentAndPolicyDrift(t *testing.T) {
	for _, name := range []string{"path", "budget", "duplicate", "counter", "root"} {
		t.Run(name, func(t *testing.T) {
			p := testPolicy()
			l := ledger{VolumeID: "11111111-1111-1111-1111-111111111111", Root: p.Root, Next: 10001, Assignments: map[string]Assignment{"bench:task": {Path: "/bench/benches/task", ProjectID: 10000, Bytes: p.BenchBytes, Inodes: p.BenchInodes}}}
			switch name {
			case "path":
				a := l.Assignments["bench:task"]
				a.Path = "/other"
				l.Assignments["bench:task"] = a
			case "budget":
				p.BenchBytes *= 2
			case "duplicate":
				l.Assignments["bench:other"] = Assignment{Path: "/bench/benches/other", ProjectID: 10000, Bytes: p.BenchBytes, Inodes: p.BenchInodes}
			case "counter":
				l.Next = 10000
			case "root":
				l.Root = "/other"
			}
			if p.checkLedger(l) == nil {
				t.Fatal("conflicting ledger accepted")
			}
		})
	}
}

func TestPolicyLoadRejectsRelativePathBeforeFilesystemWalk(t *testing.T) {
	if _, e := Load("relative-policy.json"); e == nil || !strings.Contains(e.Error(), "absolute") {
		t.Fatal("relative policy not rejected before ancestor traversal", e)
	}
}

func TestRetiredBenchKeepsItsProjectAndCannotRetireSharedCaps(t *testing.T) {
	p := testPolicy()
	l := ledger{VolumeID: "11111111-1111-1111-1111-111111111111", Root: p.Root, Next: 10001, Assignments: map[string]Assignment{"bench:task": {Path: "/bench/benches/task", ProjectID: 10000, Bytes: 0}}}
	if e := p.checkLedger(l); e != nil {
		t.Fatal("retired bench rejected", e)
	}
	l.Assignments["store"] = Assignment{Path: "/bench/store", ProjectID: 100, Bytes: 0}
	if e := p.checkLedger(l); e == nil {
		t.Fatal("shared budget could be silently removed")
	}
}

func TestInodeBudgetsMustBeExplicitAndPinned(t *testing.T) {
	for _, field := range []string{"bench", "store", "mirror", "reserve", "huge"} {
		t.Run(field, func(t *testing.T) {
			p := testPolicy()
			switch field {
			case "bench":
				p.BenchInodes = 0
			case "store":
				p.StoreInodes = 0
			case "mirror":
				p.MirrorInodes = 0
			case "reserve":
				p.ReserveInodes = 0
			case "huge":
				p.BenchInodes = (1 << 40) + 1
			}
			if e := p.Validate(); e == nil {
				t.Fatal("unsafe inode policy accepted")
			}
		})
	}
	p := testPolicy()
	l := ledger{VolumeID: "11111111-1111-1111-1111-111111111111", Root: p.Root, Next: 10001, Assignments: map[string]Assignment{"bench:task": {Path: "/bench/benches/task", ProjectID: 10000, Bytes: p.BenchBytes}}}
	if e := p.checkLedger(l); e == nil {
		t.Fatal("legacy allocation silently adopted inode caps")
	}
	p.upgradingInodes = true
	if e := p.checkLedger(l); e != nil {
		t.Fatal("explicit migration could not inspect legacy allocation", e)
	}
	p.upgradingInodes = false
	a := l.Assignments["bench:task"]
	a.Inodes = p.BenchInodes
	l.Assignments["bench:task"] = a
	if e := p.checkLedger(l); e != nil {
		t.Fatal(e)
	}
	p.BenchInodes++
	if e := p.checkLedger(l); e == nil {
		t.Fatal("inode policy drift silently accepted")
	}
}

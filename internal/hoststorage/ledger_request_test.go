package hoststorage

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func requestLedger(p Policy) ledger {
	l := ledger{VolumeID: "11111111-1111-1111-1111-111111111111", Root: p.Root, Next: 10003, Assignments: map[string]Assignment{
		"store":         {Path: filepath.Join(p.Root, "store"), ProjectID: 100, Bytes: p.StoreBytes, Inodes: p.StoreInodes},
		"mirrors":       {Path: filepath.Join(p.Root, "mirrors"), ProjectID: 101, Bytes: p.MirrorBytes, Inodes: p.MirrorInodes},
		"bench:task":    {Path: filepath.Join(p.Root, "benches", "task"), ProjectID: 10000, Bytes: p.BenchBytes, Inodes: p.BenchInodes},
		"bench:other":   {Path: filepath.Join(p.Root, "benches", "other"), ProjectID: 10001, Bytes: p.BenchBytes, Inodes: p.BenchInodes},
		"bench:retired": {Path: filepath.Join(p.Root, "benches", "retired"), ProjectID: 10002},
	}}
	return l
}

func TestLedgerConversionPreservesAssignmentsAndClosesLegacyReader(t *testing.T) {
	p := testPolicy()
	l := requestLedger(p)
	if e := p.checkLedger(l); e != nil {
		t.Fatal(e)
	}
	before, e := json.Marshal(l.Assignments)
	if e != nil {
		t.Fatal(e)
	}
	identity, next, root := l.VolumeID, l.Next, l.Root
	p.bindLedger(&l)
	if l.FormatVersion != 2 || l.FilesystemUUID != identity || l.VolumeID != "" || l.Filesystem() != identity || l.Next != next || l.Root != root {
		t.Fatal("ledger identity changed during metadata conversion", l)
	}
	if l.Binding == nil || *l.Binding != p.binding() {
		t.Fatal("policy was not pinned", l.Binding)
	}
	after, e := json.Marshal(l.Assignments)
	if e != nil || string(before) != string(after) {
		t.Fatal("conversion changed active or retired allocation", e)
	}
	if e = p.checkLedger(l); e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(l)
	if e != nil {
		t.Fatal(e)
	}
	var legacy struct{ VolumeID string }
	if e = json.Unmarshal(raw, &legacy); e != nil || legacy.VolumeID != "" {
		t.Fatal("legacy decoder still received an acceptable identity", legacy, e)
	}
	// An alternate legacy policy cannot reinterpret an indexed-bound header.
	indexed := p
	indexed.OperationJournal = "indexed-v2"
	indexed.bindLedger(&l)
	if e = p.checkLedger(l); e == nil || e.Error() != "quota policy differs from the pinned ledger; explicit migration required" {
		t.Fatalf("legacy policy accepted indexed authority: %v", e)
	}
	p.bindLedger(&l)
	// Encoding is stable on another bind, so a retry cannot move identity or IDs.
	p.bindLedger(&l)
	retry, e := json.Marshal(l)
	if e != nil || string(retry) != string(raw) {
		t.Fatal("retry changed the converted ledger", e)
	}
}

func TestVersionedLedgerAcceptsOnlyBoundedActiveBenchSizes(t *testing.T) {
	p := testPolicy()
	for _, size := range []uint64{1024, 3 * 1024, p.BenchBytes - 1024, p.BenchBytes} {
		l := requestLedger(p)
		p.bindLedger(&l)
		a := l.Assignments["bench:task"]
		a.Bytes = size
		l.Assignments["bench:task"] = a
		if e := p.checkLedger(l); e != nil {
			t.Fatal("valid smaller quota refused", size, e)
		}
	}
	for _, size := range []uint64{1, 1023, 1025, p.BenchBytes + 1024, ^uint64(0)} {
		l := requestLedger(p)
		p.bindLedger(&l)
		a := l.Assignments["bench:task"]
		a.Bytes = size
		l.Assignments["bench:task"] = a
		if e := p.checkLedger(l); e == nil {
			t.Fatal("invalid allocation passed the pinned ceiling", size)
		}
	}
	// Smaller byte counts cannot be retroactively accepted as a legacy ledger.
	l := requestLedger(p)
	a := l.Assignments["bench:task"]
	a.Bytes /= 2
	l.Assignments["bench:task"] = a
	if e := p.checkLedger(l); e == nil {
		t.Fatal("legacy uniform ledger silently adopted a custom limit")
	}
}

func TestVersionedLedgerPinsPolicyAfterAllBenchesRetire(t *testing.T) {
	p := testPolicy()
	for _, field := range []string{"OwnerUID", "OwnerGID", "BenchBytes", "StoreBytes", "MirrorBytes", "ReserveBytes", "BenchInodes", "StoreInodes", "MirrorInodes", "ReserveInodes"} {
		t.Run(field, func(t *testing.T) {
			l := requestLedger(p)
			p.bindLedger(&l)
			// Retiring every bench must not erase the original host ceiling.
			for name, a := range l.Assignments {
				if name != "store" && name != "mirrors" {
					a.Bytes, a.Inodes = 0, 0
					l.Assignments[name] = a
				}
			}
			changed := p
			v := reflect.ValueOf(&changed).Elem().FieldByName(field)
			if v.Kind() == reflect.Int {
				v.SetInt(v.Int() + 1)
			} else {
				v.SetUint(v.Uint() * 2)
			}
			if e := changed.checkLedger(l); e == nil {
				t.Fatal("policy changed without explicit migration", field)
			}
		})
	}
}

func TestVersionedLedgerIdentityAndBindingCannotBeAmbiguous(t *testing.T) {
	p := testPolicy()
	for _, fault := range []string{"missing-uuid", "bad-uuid", "legacy-uuid", "missing-binding", "future-version", "unknown-version", "legacy-new-identity", "legacy-binding"} {
		t.Run(fault, func(t *testing.T) {
			l := requestLedger(p)
			p.bindLedger(&l)
			switch fault {
			case "missing-uuid":
				l.FilesystemUUID = ""
			case "bad-uuid":
				l.FilesystemUUID = "another-volume"
			case "legacy-uuid":
				l.VolumeID = l.FilesystemUUID
			case "missing-binding":
				l.Binding = nil
			case "future-version":
				l.FormatVersion = 4
			case "unknown-version":
				l.FormatVersion = 1
			case "legacy-new-identity":
				l.FormatVersion, l.VolumeID, l.Binding = 0, l.FilesystemUUID, nil
			case "legacy-binding":
				l.FormatVersion, l.VolumeID, l.FilesystemUUID = 0, l.FilesystemUUID, ""
			}
			if e := p.checkLedger(l); e == nil {
				t.Fatal("ambiguous ledger header accepted", fault)
			}
		})
	}
}

func TestCustomBytesSurviveExplicitInodeMigration(t *testing.T) {
	p := testPolicy()
	l := requestLedger(p)
	p.bindLedger(&l)
	a := l.Assignments["bench:task"]
	a.Bytes = 3 * 1024
	l.Assignments["bench:task"] = a
	changed := p
	changed.BenchInodes *= 2
	changed.StoreInodes *= 2
	changed.MirrorInodes *= 2
	changed.ReserveInodes *= 2
	if e := changed.checkLedger(l); e == nil {
		t.Fatal("ordinary admission accepted changed inode caps")
	}
	changed.upgradingInodes = true
	if e := changed.checkLedger(l); e != nil {
		t.Fatal("explicit inode migration cannot inspect smaller byte allocation", e)
	}
	changed.bindLedger(&l)
	for name, assignment := range l.Assignments {
		if assignment.Bytes == 0 {
			continue
		}
		switch name {
		case "store":
			assignment.Inodes = changed.StoreInodes
		case "mirrors":
			assignment.Inodes = changed.MirrorInodes
		default:
			assignment.Inodes = changed.BenchInodes
		}
		l.Assignments[name] = assignment
	}
	changed.upgradingInodes = false
	if e := changed.checkLedger(l); e != nil || l.Assignments["bench:task"].Bytes != a.Bytes {
		t.Fatal("inode migration changed the requested byte cap", e)
	}
	changed.upgradingInodes = true
	changed.BenchBytes *= 2
	if e := changed.checkLedger(l); e == nil {
		t.Fatal("inode migration admitted unrelated byte-cap change")
	}
	retired := l.Assignments["bench:retired"]
	retired.Inodes = 1
	l.Assignments["bench:retired"] = retired
	changed.BenchBytes = p.BenchBytes
	if e := changed.checkLedger(l); e == nil {
		t.Fatal("inode migration resurrected a retired assignment")
	}
}

func TestRequestedBytesCannotSelectSharedCapsOrExceedCeiling(t *testing.T) {
	p := testPolicy()
	for _, size := range []uint64{0, 1024, 3 * 1024, p.BenchBytes} {
		if e := p.request("bench", size); e != nil {
			t.Fatal("valid request refused", size, e)
		}
	}
	for _, size := range []uint64{1, 1023, 1025, p.BenchBytes + 1024, ^uint64(0)} {
		if e := p.request("bench", size); e == nil {
			t.Fatal("invalid request admitted", size)
		}
	}
	for _, kind := range []string{"shared", "store", "mirrors", "../bench"} {
		if e := p.request(kind, 1024); e == nil {
			t.Fatal("size request widened its resource authority", kind)
		}
	}
}

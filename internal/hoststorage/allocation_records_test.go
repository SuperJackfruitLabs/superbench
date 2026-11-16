package hoststorage

import (
	"encoding/json"
	"testing"
)

func TestReadOnlyAllocationAbsenceIsSelectedBoundAuthority(t *testing.T) {
	p, l, anchor := storageRecords(t)
	delete(l.Assignments, "bench:source")
	read := func() (Assignment, string, error) {
		raw, _ := json.Marshal(l)
		return p.inspectStorageRecords("source", raw, anchor, true)
	}
	a, digest, err := read()
	if err != nil || a != (Assignment{Path: "/bench/benches/source"}) {
		t.Fatal(a, err)
	}
	l.Assignments["bench:other"] = Assignment{Path: "/bench/benches/other", ProjectID: 10000, Bytes: 1024, Inodes: p.BenchInodes}
	if _, same, err := read(); err != nil || same != digest {
		t.Fatal("unrelated assignment changed absence authority", err)
	}
	l.Next++
	l.Assignments["bench:source"] = Assignment{Path: a.Path, ProjectID: 10001, Bytes: 1024, Inodes: p.BenchInodes}
	if allocated, changed, err := read(); err != nil || allocated.ProjectID != 10001 || changed == digest {
		t.Fatal("new selected allocation was not distinguished", err)
	}
	retired := l.Assignments["bench:source"]
	retired.Bytes, retired.Inodes = 0, 0
	l.Assignments["bench:source"] = retired
	if _, _, err := read(); err == nil {
		t.Fatal("retired ID treated as unallocated")
	}
	delete(l.Assignments, "bench:source")
	p.OwnerUID++
	if _, _, err := read(); err == nil {
		t.Fatal("wrong policy treated as absence")
	}
}

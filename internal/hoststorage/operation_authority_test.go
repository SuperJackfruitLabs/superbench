package hoststorage

import (
	"encoding/json"
	"testing"
)

func TestOperationAuthorityPinsExistingHeaderWithoutAdoptingAssignmentChanges(t *testing.T) {
	p, l, anchor := storageRecords(t)
	raw, _ := json.Marshal(l)
	uuid, before, err := p.operationAuthorityRecords(raw, anchor)
	if err != nil || uuid != l.FilesystemUUID {
		t.Fatal(uuid, before, err)
	}
	l.Next++
	l.Assignments["bench:target"] = Assignment{Path: "/bench/benches/target", ProjectID: 10001, Bytes: 1024, Inodes: p.BenchInodes}
	updated, _ := json.Marshal(l)
	_, after, err := p.operationAuthorityRecords(updated, anchor)
	if err != nil || before != after {
		t.Fatal("unrelated valid allocation invalidated helper authority", after, err)
	}
	for _, fault := range []string{"policy", "journal", "anchor", "legacy", "corrupt"} {
		t.Run(fault, func(t *testing.T) {
			q, rows, owner := storageRecords(t)
			switch fault {
			case "policy":
				q.BenchBytes /= 2
			case "journal":
				q.OperationJournal = "indexed-v2"
			case "anchor":
				owner = []byte(`{}`)
			case "legacy":
				rows.FormatVersion = 0
				rows.VolumeID = rows.FilesystemUUID
				rows.FilesystemUUID = ""
				rows.Binding = nil
			case "corrupt":
				rows.Assignments["bench:source"] = Assignment{Path: "/wrong", ProjectID: 10000, Bytes: 1024, Inodes: q.BenchInodes}
			}
			bytes, _ := json.Marshal(rows)
			if _, _, err := q.operationAuthorityRecords(bytes, owner); err == nil {
				t.Fatal("foreign helper authority accepted")
			}
		})
	}
}

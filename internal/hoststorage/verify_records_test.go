package hoststorage

import (
	"encoding/json"
	"reflect"
	"testing"
)

func storageRecords(t *testing.T) (Policy, ledger, []byte) {
	t.Helper()
	p := testPolicy()
	l := ledger{Root: p.Root, Next: 10001, Assignments: map[string]Assignment{"bench:source": {Path: "/bench/benches/source", ProjectID: 10000, Bytes: p.BenchBytes, Inodes: p.BenchInodes}}, VolumeID: "11111111-1111-1111-1111-111111111111"}
	p.bindLedger(&l)
	anchor, _ := json.Marshal(ownerRecord{Root: p.Root, Ledger: p.Ledger})
	return p, l, anchor
}

func TestStorageLedgerSelectedBindingSurvivesUnrelatedAllocations(t *testing.T) {
	p, l, anchor := storageRecords(t)
	raw, _ := json.Marshal(l)
	a, digest, err := p.verifyStorageRecords("source", raw, anchor)
	if err != nil {
		t.Fatal(err)
	}
	l.Next++
	l.Assignments["bench:target"] = Assignment{Path: "/bench/benches/target", ProjectID: 10001, Bytes: 1024, Inodes: p.BenchInodes}
	updated, _ := json.Marshal(l)
	again, sameDigest, err := p.verifyStorageRecords("source", updated, anchor)
	if err != nil || a != again || digest != sameDigest {
		t.Fatal("unrelated allocation invalidated source authority", again, sameDigest, err)
	}
	changed := a
	changed.Bytes /= 2
	l.Assignments["bench:source"] = changed
	updated, _ = json.Marshal(l)
	_, different, err := p.verifyStorageRecords("source", updated, anchor)
	if err != nil || different == digest {
		t.Fatal("changed source allocation kept prior authority", err)
	}
}

func TestStorageLedgerRejectsMissingLegacyDriftedRetiredOrForeignAuthorityWithoutMutation(t *testing.T) {
	for _, kind := range []string{"legacy", "missing", "retired", "policy", "unrelated-invalid", "uuid", "anchor", "anchor-root", "anchor-ledger", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			p, l, anchor := storageRecords(t)
			switch kind {
			case "legacy":
				l.FormatVersion = 0
				l.VolumeID = l.FilesystemUUID
				l.FilesystemUUID = ""
				l.Binding = nil
			case "missing":
				delete(l.Assignments, "bench:source")
			case "retired":
				a := l.Assignments["bench:source"]
				a.Bytes, a.Inodes = 0, 0
				l.Assignments["bench:source"] = a
			case "policy":
				p.OwnerUID++
			case "unrelated-invalid":
				l.Assignments["bench:other"] = Assignment{Path: "/wrong", ProjectID: 10000, Bytes: 1024, Inodes: p.BenchInodes}
			case "uuid":
				l.FilesystemUUID = "not-a-volume"
			case "anchor":
				anchor = nil
			case "anchor-root":
				anchor, _ = json.Marshal(ownerRecord{Root: "/other", Ledger: p.Ledger})
			case "anchor-ledger":
				anchor, _ = json.Marshal(ownerRecord{Root: p.Root, Ledger: "/other"})
			}
			raw, _ := json.Marshal(l)
			if kind == "unknown" {
				raw = append(raw[:len(raw)-1], []byte(`,"Injected":true}`)...)
			}
			beforeRaw, beforeAnchor := append([]byte(nil), raw...), append([]byte(nil), anchor...)
			if _, _, err := p.verifyStorageRecords("source", raw, anchor); err == nil {
				t.Fatal("unsafe storage authority accepted")
			}
			if !reflect.DeepEqual(raw, beforeRaw) || !reflect.DeepEqual(anchor, beforeAnchor) {
				t.Fatal("inspection rewrote authority bytes")
			}
		})
	}
}

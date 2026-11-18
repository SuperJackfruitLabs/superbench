package hoststorage

import (
	"encoding/json"
	"testing"
)

func TestRetirementTombstoneNeedsOriginalSelectedAuthorityAndOrdinaryInspectionStillRefuses(t *testing.T) {
	p, l, anchor := storageRecords(t)
	original := l.Assignments["bench:source"]
	raw, _ := json.Marshal(l)
	_, activeDigest, retired, err := p.retirementRecords("source", raw, anchor, original)
	if err != nil || retired {
		t.Fatal("active authority", retired, err)
	}
	tombstone := original
	tombstone.Bytes, tombstone.Inodes, l.FormatVersion = 0, 0, 4 // also on a format-4 ledger
	l.Assignments["bench:source"] = tombstone
	raw, _ = json.Marshal(l)
	restored, digest, retired, err := p.retirementRecords("source", raw, anchor, original)
	if err != nil || !retired || restored != original || digest != activeDigest {
		t.Fatal("exact retired retry lost pinned authority", err)
	}
	if _, _, err = p.verifyStorageRecords("source", raw, anchor); err == nil {
		t.Fatal("ordinary storage verification adopted retired allocation")
	}
	changed := original
	changed.Bytes /= 2
	_, changedDigest, _, err := p.retirementRecords("source", raw, anchor, changed)
	if err != nil || changedDigest == activeDigest {
		t.Fatal("changed original caps retained original approval digest", err)
	}
	for _, kind := range []string{"project", "path", "unknown-row", "zero-original", "policy", "uuid"} {
		t.Run(kind, func(t *testing.T) {
			q, rows, owner := storageRecords(t)
			a := rows.Assignments["bench:source"]
			z := a
			z.Bytes, z.Inodes = 0, 0
			rows.Assignments["bench:source"] = z
			switch kind {
			case "project":
				a.ProjectID++
			case "path":
				a.Path += "-other"
			case "unknown-row":
				delete(rows.Assignments, "bench:source")
			case "zero-original":
				a = z
			case "policy":
				q.OwnerUID++
			case "uuid":
				rows.FilesystemUUID = "invalid"
			}
			encoded, _ := json.Marshal(rows)
			before := string(encoded)
			if _, _, _, err := q.retirementRecords("source", encoded, owner, a); err == nil {
				t.Fatal("foreign or reconstructed authority accepted")
			}
			if before != string(encoded) {
				t.Fatal("read-only proof rewrote ledger")
			}
		})
	}
}

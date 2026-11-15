package storageprooftest

import (
	"bytes"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

func TestExistingStorageIdentityEncodingAndAllocatedOnlyAuthority(t *testing.T) {
	s := hoststorage.StorageIdentity{Version: "superbench.storage-identity.v1", Root: "/fixed/data", BenchKey: "card", Device: 1, Inode: 2, Mount: "actual-mount", Managed: true, FilesystemUUID: "11111111-1111-1111-1111-111111111111", PolicySHA256: strings.Repeat("1", 64), LedgerSHA256: strings.Repeat("2", 64), AnchorSHA256: strings.Repeat("3", 64), ConfigSHA256: strings.Repeat("4", 64), Assignment: hoststorage.Assignment{Path: "/fixed/data/benches/card", ProjectID: 10001, Bytes: 8192, Inodes: 128}}
	raw, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"Version":"superbench.storage-identity.v1","Root":"/fixed/data","BenchKey":"card","Device":1,"Inode":2,"Mount":"actual-mount","Managed":true,"FilesystemUUID":"11111111-1111-1111-1111-111111111111","PolicySHA256":"` + strings.Repeat("1", 64) + `","LedgerSHA256":"` + strings.Repeat("2", 64) + `","AnchorSHA256":"` + strings.Repeat("3", 64) + `","ConfigSHA256":"` + strings.Repeat("4", 64) + `","Assignment":{"Path":"/fixed/data/benches/card","ProjectID":10001,"Bytes":8192,"Inodes":128}}`
	if !bytes.Equal(raw, []byte(want)) {
		t.Fatalf("existing canonical bytes changed: %s", raw)
	}
	if parsed, err := hoststorage.ParseStorageIdentity(raw); err != nil || parsed != s {
		t.Fatal(parsed, err)
	}
	for _, kind := range []string{"absent", "retired", "version", "key", "foreign-path", "digest", "unknown", "duplicate", "newline"} {
		t.Run(kind, func(t *testing.T) {
			changed := s
			switch kind {
			case "absent":
				changed.Assignment = hoststorage.Assignment{Path: s.Assignment.Path}
			case "retired":
				changed.Assignment.Bytes, changed.Assignment.Inodes = 0, 0
			case "version":
				changed.Version = "other"
			case "key":
				changed.BenchKey = "../card"
			case "foreign-path":
				changed.Assignment.Path += "-other"
			case "digest":
				changed.LedgerSHA256 = strings.Repeat("A", 64)
			}
			if kind == "unknown" || kind == "duplicate" || kind == "newline" {
				bad := append([]byte(nil), raw...)
				switch kind {
				case "unknown":
					bad = append(bad[:len(bad)-1], []byte(`,"unknown":1}`)...)
				case "duplicate":
					bad = append(bad[:len(bad)-1], []byte(`,"Managed":true}`)...)
				case "newline":
					bad = append(bad, '\n')
				}
				if _, err := hoststorage.ParseStorageIdentity(bad); err == nil {
					t.Fatal("invalid original identity accepted")
				}
			} else if _, err := changed.Bytes(); err == nil {
				t.Fatal("invalid original identity accepted")
			}
		})
	}
}

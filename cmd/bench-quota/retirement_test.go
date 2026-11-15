package main

import (
	"bytes"
	"encoding/json"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"os"
	"strings"
	"testing"
)

func helperRetirementApproval(t *testing.T) []byte {
	t.Helper()
	s := hoststorage.StorageIdentity{Version: "superbench.storage-identity.v1", Root: "/fixed-data", BenchKey: "source", Device: 1, Inode: 2, Mount: "fixed-mount", Managed: true, FilesystemUUID: "11111111-1111-1111-1111-111111111111", PolicySHA256: strings.Repeat("1", 64), LedgerSHA256: strings.Repeat("2", 64), AnchorSHA256: strings.Repeat("3", 64), ConfigSHA256: strings.Repeat("4", 64), Assignment: hoststorage.Assignment{Path: "/fixed-data/benches/source", ProjectID: 10000, Bytes: 1024, Inodes: 128}}
	a := hoststorage.RetirementApproval{Version: hoststorage.RetirementVersion, Key: "source", Storage: s, Root: hoststorage.RetirementRoot{Absent: true, ParentDevice: 1, ParentInode: 3, ParentMode: uint32(os.ModeDir) | 0755}}
	raw, err := a.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestApprovedRetirementHelperRejectsUnboundedAmbiguousOrDifferentKeyBeforeEffects(t *testing.T) {
	valid := helperRetirementApproval(t)
	for _, kind := range []string{"oversized", "duplicate", "unknown", "different-key", "trailing", "operation"} {
		t.Run(kind, func(t *testing.T) {
			raw, key, operation := valid, "source", "retire-approved"
			switch kind {
			case "oversized":
				raw = []byte(strings.Repeat("x", hoststorage.MaxRetirementApproval+2))
			case "duplicate":
				raw = append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"Key":"source"}`)...)
			case "unknown":
				var v map[string]any
				_ = json.Unmarshal(valid, &v)
				v["ArbitraryPath"] = "/unapproved"
				raw, _ = json.Marshal(v)
			case "different-key":
				key = "another"
			case "trailing":
				raw = append(append([]byte(nil), valid...), valid...)
			case "operation":
				operation = "arbitrary"
			}
			var output bytes.Buffer
			input := bytes.NewReader(raw)
			if err := runRetirement(hoststorage.Policy{}, operation, key, input, &output); err == nil {
				t.Fatal("invalid approval reached mutation")
			}
			if output.Len() != 0 {
				t.Fatal("failed approval emitted success output")
			}
			if len(raw)-input.Len() > hoststorage.MaxRetirementApproval+1 {
				t.Fatal("helper consumed unbounded input")
			}
		})
	}
	for _, args := range [][]string{{"retire-approved", "shared", "store"}, {"verify-retired", "bench", "source", "/arbitrary"}, {"retire-approved", "bench"}, {"verify-retired", "bench", "source", "10000", "1024"}} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatal("non-fixed retirement command shape reached policy", args, err)
		}
	}
}

package mirrorstage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestLoadRefusesUnsafeOrAmbiguousRecordsWithoutChangingStage(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "oversized", "public", "duplicate", "unknown", "wrong-root-id", "missing-root"} {
		t.Run(kind, func(t *testing.T) {
			state, parent, mirror := fixture(t)
			p, e := Create(state, parent, mirror)
			if e != nil {
				t.Fatal(e)
			}
			sentinel := filepath.Join(p.Stage, "uncheckpointed")
			if e = os.WriteFile(sentinel, []byte("preserve exactly"), 0600); e != nil {
				t.Fatal(e)
			}
			record := filepath.Join(state, "mirror-stages", p.ID+"-root.json")
			raw, e := os.ReadFile(record)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "symlink":
				if e = os.Rename(record, record+"-retained"); e != nil {
					t.Fatal(e)
				}
				e = os.Symlink(record+"-retained", record)
			case "fifo":
				if e = os.Remove(record); e != nil {
					t.Fatal(e)
				}
				e = syscall.Mkfifo(record, 0600)
			case "oversized":
				e = os.WriteFile(record, []byte(strings.Repeat(" ", MaxRecordBytes+1)), 0600)
			case "public":
				e = os.Chmod(record, 0644)
			case "duplicate":
				e = os.WriteFile(record, []byte(`{"ID":"foreign",`+string(raw[1:])), 0600)
			case "unknown":
				e = os.WriteFile(record, []byte(`{"Extra":true,`+string(raw[1:])), 0600)
			case "wrong-root-id":
				var r Root
				if e = json.Unmarshal(raw, &r); e != nil {
					t.Fatal(e)
				}
				r.ID = strings.Repeat("f", 32)
				raw, e = json.Marshal(r)
				if e != nil {
					t.Fatal(e)
				}
				e = os.WriteFile(record, raw, 0600)
			case "missing-root":
				e = os.Remove(record)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, _, e = Load(state, parent, p.ID); e == nil {
				t.Fatal("adopted ambiguous authority", kind)
			}
			raw, e = os.ReadFile(sentinel)
			if e != nil || string(raw) != "preserve exactly" {
				t.Fatal("changed stage", e)
			}
			if _, e = os.Stat(filepath.Join(state, "mirror-stages", p.ID+".json")); e != nil {
				t.Fatal("lost intent", e)
			}
		})
	}
}

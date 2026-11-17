package freshreadiness

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

func TestStorageRequiresAReadyIndexedJournal(t *testing.T) {
	opened := 0
	open := func() error { opened++; return nil }
	if err := ready(hoststorage.Policy{OperationJournal: IndexedJournal}, nil, open); err != nil || opened != 1 {
		t.Fatal("ready indexed journal refused", err, opened)
	}
	// The pilot host's legacy policy: every Fresh create failed at materializing.
	err := ready(hoststorage.Policy{}, nil, open)
	if err == nil || !strings.Contains(err.Error(), `"OperationJournal": "indexed-v2"`) || !strings.Contains(err.Error(), "--storage") || opened != 1 {
		t.Fatal("legacy policy accepted or unclear", err)
	}
	missing := errors.New("no such file")
	if err = ready(hoststorage.Policy{OperationJournal: IndexedJournal}, missing, open); !errors.Is(err, missing) || !strings.Contains(err.Error(), StoragePolicy) {
		t.Fatal("unreadable policy accepted", err)
	}
	// A half-finished initialize-indexed: the policy is indexed but the helper
	// cannot open a journal without its ready record.
	partial := errors.New("bench-quota: missing ready record")
	if err = ready(hoststorage.Policy{OperationJournal: IndexedJournal}, nil, func() error { return partial }); !errors.Is(err, partial) || !strings.Contains(err.Error(), "not ready") {
		t.Fatal("half-initialized journal accepted", err)
	}
}

// The installer writes storage.json; the helper's strict decoder and this check
// must accept its exact bytes, and the ledger must sit in the private directory
// tmpfiles creates, which the fresh initializer requires of the ledger's parent.
func TestInstallerStorageDocumentIsIndexed(t *testing.T) {
	installer, err := filepath.Abs(filepath.Join("..", "..", "deploy", "linux"))
	if err != nil {
		t.Fatal(err)
	}
	script := `import sys
sys.path.insert(0, sys.argv[1])
import install
budgets = {"BenchBytes": 1073741824, "StoreBytes": 2147483648, "MirrorBytes": 1073741824,
           "ReserveBytes": 1073741824, "BenchInodes": 100000, "StoreInodes": 1000000,
           "MirrorInodes": 500000, "ReserveInodes": 100000}
sys.stdout.buffer.write(install.storage_document((977, 977), budgets)[install.STORAGE_FILE][0])`
	out, err := exec.Command("python3", "-I", "-c", script, installer).Output()
	if err != nil {
		t.Fatal(err)
	}
	var p hoststorage.Policy
	decoder := json.NewDecoder(bytes.NewReader(out))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&p); err != nil {
		t.Fatal(err)
	}
	if err = p.Validate(); err != nil {
		t.Fatal(err)
	}
	if err = ready(p, nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p.Root != "/srv/superbench-data" || filepath.Dir(p.Ledger) != "/var/lib/superbench-quota" || p.OwnerUID != 977 || p.OwnerGID != 977 || p.BenchInodes != 100000 {
		t.Fatalf("unexpected installed policy %+v", p)
	}
}

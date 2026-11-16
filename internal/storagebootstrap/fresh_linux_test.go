//go:build linux

package storagebootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationjournal"
)

func TestEmptyStorageObservationRejectsEveryExistingLeafAndPreservesBytes(t *testing.T) {
	for _, kind := range []string{"directory", "regular", "symlink", "anchor", "unknown-data"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "data")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			r, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			p, err := os.Open(base)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			ledger := filepath.Join(base, "ledger")
			f := Fresh{policy: hoststorage.Policy{Root: root, Ledger: ledger}, root: r, parent: p}
			if err := f.Empty(); err != nil {
				t.Fatalf("actual leaf absence baseline refused: %v", err)
			}
			var target string
			switch kind {
			case "directory":
				target = filepath.Join(ledger, "sentinel")
				if err = os.Mkdir(ledger, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target = filepath.Join(base, "evidence")
				if err = os.Symlink(target, ledger); err != nil {
					t.Fatal(err)
				}
			case "anchor":
				target = filepath.Join(root, ".superbench-quota-owner.json")
			case "unknown-data":
				target = filepath.Join(root, "unknown")
			default:
				target = ledger
			}
			if err = os.WriteFile(target, []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = f.Empty(); !errors.Is(err, ErrFresh) {
				t.Fatalf("existing %s accepted: %v", kind, err)
			}
			raw, err := os.ReadFile(target)
			if err != nil || string(raw) != "retained" {
				t.Fatalf("observation changed evidence: %q %v", raw, err)
			}
		})
	}
}

func TestZeroFreshAuthorityCannotInitializeOrEncodeAllocationRecords(t *testing.T) {
	var f Fresh
	if err := f.Initialize(context.Background(), quotaoperationjournal.FreshLedger{}); !errors.Is(err, ErrFresh) {
		t.Fatalf("zero initializer: %v", err)
	}
	p := hoststorage.Policy{OperationJournal: "indexed-v2", Root: "/fresh-data", Ledger: "/fresh-ledger", BenchBytes: 1024, StoreBytes: 1024, MirrorBytes: 1024, ReserveBytes: 1024, BenchInodes: 1, StoreInodes: 1, MirrorInodes: 1, ReserveInodes: 1}
	header, anchor, err := p.FreshAllocationRecords(quotaoperationjournal.FreshLedger{}, "11111111-1111-1111-1111-111111111111")
	if !errors.Is(err, quotaoperationjournal.ErrAuthority) || header != nil || anchor != nil {
		t.Fatalf("invented fresh ledger encoded authority: %s %s %v", header, anchor, err)
	}
}

func TestFreshProjectInventoryRejectsHistoricalIDsEvenWhenEmpty(t *testing.T) {
	for _, valid := range []string{"", "#0 0 0 0 00 [--------]\n"} {
		if err := freshProjects(valid); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{"#10000 0 0 0", "#0 0 0 128", "unexpected report", "#0 invalid 0 0", "#0 0 0 0\n#101 0 0 0"} {
		if err := freshProjects(bad); !errors.Is(err, ErrFresh) {
			t.Fatalf("historical project inventory accepted %q: %v", bad, err)
		}
	}
}

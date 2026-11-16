package hoststorage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRetainedBootstrapIntentBlocksLegacyAllocationRegardlessOfContents(t *testing.T) {
	p := Policy{Ledger: t.TempDir()}
	if err := p.refuseRegisteredAllocation("card"); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(p.Ledger, "payload-bootstrap")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "card.json")
	// Partial bytes still prohibit acquisition. This guard must not parse a failed
	// intent into permission to retry legacy allocation ownership changes.
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.refuseRegisteredAllocation("card"); !errors.Is(err, ErrRegisteredAllocation) {
		t.Fatalf("legacy ownership allowed: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", path); err != nil {
		t.Fatal(err)
	}
	if err := p.refuseRegisteredAllocation("card"); !errors.Is(err, ErrRegisteredAllocation) {
		t.Fatalf("symlink intent allowed: %v", err)
	}
}

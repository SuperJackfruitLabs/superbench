package storageprooftest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

func TestUnmanagedAllocationInspectionDoesNotCreateOrInventAnAssignment(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := hoststorage.Worker{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data"), Apply: func(hoststorage.HostConfig, string, string, uint64) (hoststorage.Assignment, error) {
		t.Fatal("inspection called Apply")
		return hoststorage.Assignment{}, nil
	}}
	for _, p := range []string{w.State, w.Data} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	o, err := w.InspectAllocation(context.Background(), "card")
	if err != nil {
		t.Fatal(err)
	}
	if o.Managed || o.Allocated() || o.Assignment.ProjectID != 0 || o.Assignment.Path != filepath.Join(w.Data, "benches", "card") {
		t.Fatal("invented managed allocation", o)
	}
	if err := w.VerifyAllocation(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	raw, err := o.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := hoststorage.ParseAllocationObservation(raw); err != nil || parsed != o {
		t.Fatal(parsed, err)
	}
	if _, err := hoststorage.ParseStorageIdentity(raw); err == nil {
		t.Fatal("allocation observation adopted as allocated identity")
	}
	for _, p := range []string{w.State, w.Data} {
		entries, err := os.ReadDir(p)
		if err != nil || len(entries) != 0 {
			t.Fatal("inspection changed directories", entries, err)
		}
	}
	if err := os.Rename(w.Data, w.Data+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(w.Data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := w.VerifyAllocation(context.Background(), o); err == nil {
		t.Fatal("replaced storage authority accepted")
	}
}

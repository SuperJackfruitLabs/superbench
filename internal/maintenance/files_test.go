package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsafePolicyFilesRefuseBeforeCollection(t *testing.T) {
	for _, kind := range []string{"symlink", "public", "duplicate-json", "unknown-field", "trailing-json"} {
		t.Run(kind, func(t *testing.T) {
			m, store, p := fixture(t)
			proposal, e := m.Preview(p)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = m.Enable(*proposal, true); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(m.Root, "retention-policy.json")
			switch kind {
			case "symlink":
				if e = os.Rename(path, path+".target"); e == nil {
					e = os.Symlink(path+".target", path)
				}
			case "public":
				e = os.Chmod(path, 0644)
			case "duplicate-json":
				e = os.WriteFile(path, []byte(`{"Enabled":true,"Enabled":false}`), 0600)
			case "unknown-field":
				e = os.WriteFile(path, []byte(`{"Unknown":true}`), 0600)
			case "trailing-json":
				e = os.WriteFile(path, []byte(`{} {}`), 0600)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = m.Tick(context.Background()); e == nil || len(store.calls) != 0 {
				t.Fatal("unsafe policy reached collector", e)
			}
		})
	}
}

func TestCanceledContextAndBusyGateDoNotStartCollection(t *testing.T) {
	m, store, p := fixture(t)
	proposal, e := m.Preview(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Enable(*proposal, true); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = m.Tick(ctx); e != context.Canceled || len(store.calls) != 0 {
		t.Fatal("canceled cycle reached collector", e)
	}
	unlock, e := m.gate(false)
	if e != nil {
		t.Fatal(e)
	}
	r, e := m.Tick(context.Background())
	unlock()
	if e != nil || r.Status != "busy" || len(store.calls) != 0 {
		t.Fatal("parallel cycle reached collector", r, e)
	}
}

func TestOrphanPointerAcknowledgmentDoesNotDeleteOrReapprove(t *testing.T) {
	m, store, p := fixture(t)
	proposal, e := m.Preview(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Enable(*proposal, true); e != nil {
		t.Fatal(e)
	}
	if e = m.write("retention-auto-pending.json", Pending{PolicyDigest: proposal.Digest, Plan: proposal.Initial}); e != nil {
		t.Fatal(e)
	}
	changed := *proposal
	changed.Policy.MaxBytes *= 2
	changed.Digest, _ = changed.Hash()
	if _, e = m.Enable(changed, true); e == nil {
		t.Fatal("new policy bypassed pending batch")
	}
	status, e := m.AcknowledgePending()
	if e != nil || status.Pending != nil || len(store.calls) != 0 || len(store.items) != 1 {
		t.Fatal("acknowledgment changed output", status, e)
	}
}

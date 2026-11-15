package operationcancel

import (
	"errors"
	"testing"
)

func TestNoEffectsRequiresCompleteAdmissionAbsenceAndBarrier(t *testing.T) {
	m, db, i, b, f := fixture(t)
	token := b.Lease.Token
	if err := m.NoEffects(i.Target); err == nil {
		t.Fatal("missing barrier accepted")
	}
	m.CheckBarrier = func() error { return nil }
	if err := m.NoEffects(i.Target); err == nil {
		t.Fatal("existing admission called no effects")
	}
	if _, err := db.Exec(`DELETE FROM workspace_operations`); err != nil {
		t.Fatal(err)
	}
	if err := m.NoEffects(i.Target); err == nil {
		t.Fatal("orphan run provenance called no effects")
	}
	if _, err := db.Exec(`DELETE FROM workspace_operation_runs`); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("cancel authority changed")
	checks := 0
	m.CheckBarrier = func() error {
		checks++
		if checks == 2 {
			return denied
		}
		return nil
	}
	if err := m.NoEffects(i.Target); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	m.CheckBarrier = func() error { return nil }
	if err := m.NoEffects(i.Target); err != nil {
		t.Fatal(err)
	}
	if f.count != 0 || b.Lease.Token != token {
		t.Fatal("no-effect proof touched owner", f.count, b.Lease)
	}
}

package operationcancel

import (
	"os"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestCancellationRevokesExactOwnerThenRequiresActualSealedHistory(t *testing.T) {
	m, db, i, b, f := fixture(t)
	b.Lease.StopRequested = false
	b.Lease.Continuation = strings.Repeat("f", 32)
	b.Lease.Execution.Phase = "running"
	if err := os.WriteFile(b.Lease.Execution.Log, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Revoke(i); err != nil {
		t.Fatal(err)
	}
	if !b.Lease.StopRequested || b.Lease.Continuation != "" || b.Lease.Execution.Phase != "stopped" || f.count != 1 {
		t.Fatal(b, f.count)
	}
	if err := m.Conclude(i); err == nil {
		t.Fatal("unsealed run certified cancelled")
	}
	if err := m.ReconcileInterruption(i); err != nil {
		t.Fatal(err)
	}
	if err := m.Conclude(i); err != nil {
		t.Fatal(err)
	}
	binding, err := operationcontrol.Get(db, i.Target)
	if err != nil || binding.Phase != "stopped" {
		t.Fatal(binding, err)
	}
}
func TestWrongOwnerCannotCauseFenceOrRevocation(t *testing.T) {
	m, _, i, b, f := fixture(t)
	b.Lease.StopRequested = false
	b.Lease.Continuation = strings.Repeat("f", 32)
	b.Lease.Token = strings.Repeat("9", 32)
	if err := m.Revoke(i); err == nil {
		t.Fatal("later owner stopped")
	}
	if f.count != 0 || b.Lease.StopRequested || b.Lease.Continuation == "" {
		t.Fatal("wrong owner affected", b, f.count)
	}
}

func TestCancellationPreservesChangedCaptureAndFailedTerminalCommit(t *testing.T) {
	m, db, i, b, _ := fixture(t)
	if err := os.WriteFile(b.Lease.Execution.Log, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.ReconcileInterruption(i); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Lease.Execution.Log, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Conclude(i); err == nil {
		t.Fatal("changed sealed bytes certified")
	}
	if err := os.WriteFile(b.Lease.Execution.Log, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	// Content equality alone does not restore exact metadata if inode/mode changed;
	// this write preserves both and the original sealed hash and size.
	if _, err := db.Exec(`CREATE TRIGGER ignore_terminal BEFORE UPDATE OF phase ON workspace_operations BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if err := m.Conclude(i); err == nil {
		t.Fatal("ignored terminal write acknowledged")
	}
	got, err := operationcontrol.Get(db, i.Target)
	if err != nil || got.Phase != "bound" {
		t.Fatal(got, err)
	}
	if _, err = db.Exec(`DROP TRIGGER ignore_terminal`); err != nil {
		t.Fatal(err)
	}
	if err = m.Conclude(i); err != nil {
		t.Fatal(err)
	}
}
func TestCancellationPreflightPinsAuthorityAcrossHelperWait(t *testing.T) {
	m, db, i, b, f := fixture(t)
	before, err := m.Inspect(i)
	if err != nil {
		t.Fatal(err)
	}
	m.Expected = &before
	if _, err = db.Exec(`UPDATE workspace_operations SET control_boot='different'`); err != nil {
		t.Fatal(err)
	}
	if err = m.Revoke(i); err == nil {
		t.Fatal("changed original scope fenced")
	}
	if f.count != 0 || !b.Lease.StopRequested {
		t.Fatal("preflight changed owner", f.count, b)
	}
}

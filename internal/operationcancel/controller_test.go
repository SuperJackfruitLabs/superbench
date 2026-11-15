package operationcancel

import (
	"context"
	"os"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestPrivateControllerSealsUnknownThenAllowsOnlyOldEpochRetry(t *testing.T) {
	m, db, i, b, f := fixture(t)
	b.Lease.StopRequested = false
	b.Lease.Continuation = strings.Repeat("f", 32)
	b.Lease.Execution.Phase = "running"
	if err := os.WriteFile(b.Lease.Execution.Log, []byte("actual incomplete output"), 0600); err != nil {
		t.Fatal(err)
	}
	gitChecks := 0
	m.RecoverGit = func(c operationcontrol.Control, got operationcontrol.Intent) error {
		if got != i || c.State != m.State {
			t.Fatal(c, got)
		}
		gitChecks++
		return nil
	}
	if err := m.Reconcile(context.Background(), i, nil); err != nil {
		t.Fatal(err)
	}
	oldFences := f.count
	if oldFences != 2 || gitChecks != 1 || !b.Lease.StopRequested || b.Lease.Continuation != "" {
		t.Fatal(oldFences, gitChecks, b)
	}
	record, err := operationcontrol.Get(db, i.Target)
	if err != nil || record.Phase != "stopped" {
		t.Fatal(record, err)
	}
	// SQL terminal commit can precede service confirmation. A later owner's
	// capability must remain untouched when that old cancellation is retried.
	b.Lease = &model.Lease{Holder: "later", Token: strings.Repeat("9", 32), Continuation: strings.Repeat("8", 32)}
	if err = m.Reconcile(context.Background(), i, nil); err != nil {
		t.Fatal(err)
	}
	if f.count != oldFences || b.Lease.Holder != "later" || b.Lease.StopRequested || b.Lease.Continuation == "" {
		t.Fatal("later writer affected", f.count, b)
	}
}
func TestMalformedHelperVectorRefusesBeforeAnyGitOrFenceEffect(t *testing.T) {
	m, _, i, _, f := fixture(t)
	gitChecks := 0
	m.RecoverGit = func(operationcontrol.Control, operationcontrol.Intent) error { gitChecks++; return nil }
	if err := m.Reconcile(context.Background(), i, []operationcontrol.Helper{{}}); err == nil {
		t.Fatal("unbound helper accepted")
	}
	if gitChecks != 0 || f.count != 0 {
		t.Fatal("invalid vector caused effects", gitChecks, f.count)
	}
}

package operationreceipts

import (
	"context"
	"fmt"
	"testing"
)

func TestRestartCursorDoesNotStarveBehindUnreconciledBatch(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	for n := 0; n < 65; n++ {
		i := cutoffIntent(fmt.Sprintf("queued-%02d", n))
		if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
			t.Fatal(err)
		}
		_, stop, e := r.operationScope(context.Background(), i)
		if e != nil {
			t.Fatal(e)
		}
		stop()
		if _, err = r.requestCancelV2(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
			t.Fatal(err)
		}
	}
	first, next, err := r.ReconciliationBatch(WorkCursor{})
	if err != nil || len(first) != 64 || next.RequestID != "queued-63" {
		t.Fatal(len(first), next, err)
	}
	// None of the first batch has completed; queued helper waits must not starve
	// the later admitted operation merely because earlier rows remain pending.
	last, end, err := r.ReconciliationBatch(next)
	if err != nil || len(last) != 1 || last[0].Intent.Target.RequestID != "queued-64" || end != (WorkCursor{}) {
		t.Fatal(last, end, err)
	}
	if last[0].Completed {
		t.Fatal("requested cancellation considered completed")
	}
}

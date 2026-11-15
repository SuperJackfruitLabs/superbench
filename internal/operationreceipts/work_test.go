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

func TestRestartInventoryIncludesUnknownTombstonesWithoutInventingEpoch(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	for _, id := range []string{"a-pending", "b-terminal", "c-pending"} {
		target := cutoffIntent(id).Target
		if _, err = r.requestCancelV2(target.UID, target.RequestID, target.Digest); err != nil {
			t.Fatal(err)
		}
		if id == "b-terminal" {
			snapshot, err := r.NoEffectsInventory(target)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.ConfirmNoEffects(snapshot); err != nil {
				t.Fatal(err)
			}
		}
	}
	batch, end, err := r.ReconciliationBatch(WorkCursor{})
	if err != nil || len(batch) != 2 || end != (WorkCursor{}) {
		t.Fatal(batch, end, err)
	}
	if batch[0].Target.RequestID != "a-pending" || batch[1].Target.RequestID != "c-pending" {
		t.Fatal(batch)
	}
	for _, w := range batch {
		if !w.Target.Valid() || w.Intent.Valid() || w.Completed {
			t.Fatal("tombstone invented launch/completion authority", w)
		}
	}
}

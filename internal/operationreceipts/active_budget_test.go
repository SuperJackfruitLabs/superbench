package operationreceipts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Synthetic durable historical rows characterize admission/replay accounting;
// these fixtures do not assert that a live writer was fenced by SQL alone.
func retainedHistory(t *testing.T, r *receipts, count int, finalized int) {
	t.Helper()
	_, err := r.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x+1<?)
 INSERT INTO receipts(owner,id,digest,state,body,status) SELECT 9,printf('history-%05d',x),?,'done',?,207 FROM n`, count, strings.Repeat("a", 64), []byte(" \n{\"original\":true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.db.Exec(`INSERT INTO operation_intents(owner,id,digest,key,operation,epoch,helper_tracking,core_finalized)
 SELECT owner,id,digest,'card','lease',?,1,? FROM receipts WHERE id LIKE 'history-%'`, strings.Repeat("b", 32), finalized)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalReceiptCountAndBytesDoNotConsumeActiveBudget(t *testing.T) {
	root := t.TempDir()
	r, err := openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	retainedHistory(t, r, 10001, 1)
	admit := func(id string) {
		t.Helper()
		i := cutoffIntent(id)
		cached, _, stop, e := r.ClaimScope(context.Background(), i)
		if e != nil || cached != nil || stop == nil {
			t.Fatal("terminal history blocked fresh typed admission", cached, e)
		}
		stop()
		if e = r.finish(i.Target.UID, i.Target.RequestID, []byte(`{}`), 200); e != nil {
			t.Fatal(e)
		}
		if e = r.markCoreFinalized(i); e != nil {
			t.Fatal(e)
		}
	}
	admit("after-count")
	// More than64MiB of individually valid historical responses must not be
	// confused with the bounded reservation for unresolved work.
	large := []byte(`"` + strings.Repeat("x", (64<<10)-2) + `"`)
	if _, err = r.db.Exec(`UPDATE receipts SET body=? WHERE id>='history-00000' AND id<='history-01024'`, large); err != nil {
		t.Fatal(err)
	}
	admit("after-bytes")
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, id := range []string{"history-00000", "history-10000"} {
		i := cutoffIntent(id)
		old, found, e := r.lookup(i.Target.UID, id, i.Target.Digest)
		expected := []byte(" \n{\"original\":true}\n")
		if id == "history-00000" {
			expected = large
		}
		if e != nil || !found || old == nil || old.status != 207 || !bytes.Equal(old.body, expected) {
			t.Fatal("historical evidence changed", id, e)
		}
		wrong := i.Target
		wrong.Digest = strings.Repeat("c", 64)
		if _, _, e = r.lookup(wrong.UID, wrong.RequestID, wrong.Digest); !errors.Is(e, errConflict) {
			t.Fatal("historical digest reused", e)
		}
		if old, found, e = r.lookup(i.Target.UID+1, id, i.Target.Digest); e != nil || found || old != nil {
			t.Fatal("historical cross-UID disclosure", e)
		}
	}
}

func TestUnprovenDoneAndInterruptedReceiptsStillReserveActiveBudget(t *testing.T) {
	for _, phase := range []string{"done-unfinalized", "interrupted-v1"} {
		t.Run(phase, func(t *testing.T) {
			r, err := openReceipts(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			known := cutoffIntent("known-active")
			_, _, stop, e := r.ClaimScope(context.Background(), known)
			if e != nil {
				t.Fatal(e)
			}
			defer stop()
			if phase == "done-unfinalized" {
				retainedHistory(t, r, 1023, 0)
			} else {
				_, err = r.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<1022)
   INSERT INTO receipts(owner,id,digest,state) SELECT 9,printf('unknown-%05d',x),?,'interrupted' FROM n`, strings.Repeat("a", 64))
				if err != nil {
					t.Fatal(err)
				}
			}
			i := cutoffIntent("must-not-grow")
			if cached, _, stop, e := r.ClaimScope(context.Background(), i); !errors.Is(e, errCapacity) || cached != nil || stop != nil {
				t.Fatal("unproven old effects lost reservation", cached, e)
			}
			if _, found, e := r.lookup(i.Target.UID, i.Target.RequestID, i.Target.Digest); e != nil || found {
				t.Fatal("capacity refusal admitted identity", e)
			}
			if _, e := r.claim(known.Target.UID, "scalar-must-not-grow", known.Target.Digest); !errors.Is(e, errCapacity) {
				t.Fatal("legacy claim bypassed active budget", e)
			}
			if state, e := r.requestCancelV2(known.Target.UID, known.Target.RequestID, known.Target.Digest); e != nil || state != "cancel_requested" {
				t.Fatal("active capacity blocked known cancellation", state, e)
			}
			if _, e := r.claim(known.Target.UID, known.Target.RequestID, known.Target.Digest); !errors.Is(e, errCancelRequested) {
				t.Fatal("known cancellation lost replay barrier", e)
			}
		})
	}
}

func TestFinalizedHistoryDoesNotHidePendingReconciliationOrTerminalTombstones(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	retainedHistory(t, r, 10001, 1)
	// Confirmed tombstones are permanent denial evidence, not pending work.
	_, err = r.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<1024)
 INSERT INTO operation_cancels(owner,id,digest,state) SELECT 9,printf('cancelled-%05d',x),?,'cancelled' FROM n`, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"z-pending-a", "z-pending-b"} {
		i := cutoffIntent(id)
		if state, e := r.requestCancelV2(i.Target.UID, id, i.Target.Digest); e != nil || state != "cancel_requested" {
			t.Fatal("terminal tombstones consumed cancellation capacity", state, e)
		}
	}
	batch, end, err := r.ReconciliationBatch(WorkCursor{})
	if err != nil || len(batch) != 2 || end != (WorkCursor{}) {
		t.Fatal("historical rows hid pending work", batch, end, err)
	}
	for n, w := range batch {
		if w.Target.RequestID != fmt.Sprintf("z-pending-%c", 'a'+n) || w.Intent.Valid() || w.Completed {
			t.Fatal("pending tombstone fabricated launch authority", w)
		}
	}
	if pending, err := r.completedOperations(); err != nil || len(pending) != 0 {
		t.Fatal("finalized history reentered core work", pending, err)
	}
}

func TestMalformedHistoricalRelationRefusesLookupAndReopen(t *testing.T) {
	for _, fault := range []string{"digest", "missing-receipt", "finalized-running"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			r, err := openReceipts(root)
			if err != nil {
				t.Fatal(err)
			}
			retainedHistory(t, r, 1, 1)
			i := cutoffIntent("history-00000")
			query := `UPDATE operation_intents SET digest='` + strings.Repeat("c", 64) + `'`
			if fault == "missing-receipt" {
				query = `DELETE FROM receipts`
			}
			if fault == "finalized-running" {
				query = `UPDATE receipts SET state='running',body=NULL,status=NULL`
			}
			if _, err = r.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if _, _, err = r.lookup(i.Target.UID, i.Target.RequestID, i.Target.Digest); !errors.Is(err, errInDoubt) {
				t.Fatal("corrupt historical relation disclosed success", err)
			}
			if err = r.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, e := openReceipts(root); !errors.Is(e, errInDoubt) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatal("reopen adopted corrupt terminal relation", e)
			}
		})
	}
}

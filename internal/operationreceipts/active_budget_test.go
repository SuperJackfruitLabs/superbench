package operationreceipts

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"path/filepath"
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

func TestDerivedIndexesDoNotChangeSchemaOrHistoricalBytes(t *testing.T) {
	root := t.TempDir()
	r, err := openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.claim(12, "legacy", "opaque-hash"); err != nil {
		t.Fatal(err)
	}
	body := []byte(" \n{\"historical\":true}\n")
	if err = r.finish(12, "legacy", body, 209); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`DROP INDEX receipt_unresolved;DROP INDEX intent_unresolved;DROP INDEX cancel_unresolved`); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r, err = openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var version, count int
	if err = r.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 6 {
		t.Fatal("derived index changed authority format", version, err)
	}
	if err = r.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name IN ('receipt_unresolved','intent_unresolved','cancel_unresolved')`).Scan(&count); err != nil || count != 3 {
		t.Fatal("missing derived indexes", count, err)
	}
	original, found, err := r.lookup(12, "legacy", "opaque-hash")
	if err != nil || !found || original.status != 209 || !bytes.Equal(original.body, body) {
		t.Fatal("index rebuilding rewrote historical response", err)
	}
}

func TestHistoricalHelpersDoNotConsumeNewRequestInventory(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	retainedHistory(t, r, 10001, 1)
	tx, err := r.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO operation_helpers(owner,id,kind,key,invocation) VALUES(?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 10001; n++ {
		i := cutoffIntent(fmt.Sprintf("history-%05d", n))
		h := helperFixture(i)
		for _, kind := range []string{"bench", "store", "mirrors"} {
			h.Kind, h.Key, h.Requested = "bench", i.Key, 1024
			if kind != "bench" {
				h.Kind, h.Key, h.Requested = "shared", kind, 0
			}
			raw, e := json.Marshal(h)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = stmt.Exec(i.Target.UID, i.Target.RequestID, h.Kind, h.Key, string(raw)); e != nil {
				t.Fatal(e)
			}
		}
	}
	stmt.Close()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	i := cutoffIntent("new-helper")
	_, _, stop, err := r.ClaimScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	h := helperFixture(i)
	if err = r.recordOperationHelper(i, h); err != nil {
		t.Fatal("historical helper count blocked new owned inventory", err)
	}
	if got, e := r.operationHelpers(i); e != nil || len(got) != 1 || got[0] != h {
		t.Fatal("new exact helper inventory changed", got, e)
	}
	if got, e := r.operationHelpers(cutoffIntent("history-00000")); e != nil || len(got) != 3 {
		t.Fatal("new helper damaged historical evidence", got, e)
	}
	changed := h
	changed.Requested *= 2
	if err = r.recordOperationHelper(i, changed); !errors.Is(err, errConflict) {
		t.Fatal("inventory accounting allowed authority replacement", err)
	}
}

func TestIndexCreationFailureDoesNotAcknowledgeOrRewriteHistory(t *testing.T) {
	root := t.TempDir()
	r, err := openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.claim(12, "legacy", "opaque"); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"unchanged":true}`)
	if err = r.finish(12, "legacy", body, 201); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`DROP INDEX intent_unresolved;CREATE TABLE intent_unresolved(x)`); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if failed, e := openReceipts(root); e == nil || !strings.Contains(e.Error(), "there is already a table named intent_unresolved") {
		if failed != nil {
			failed.Close()
		}
		t.Fatal("index conflict did not refuse opening", e)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "service.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got []byte
	var status, version int
	if err = db.QueryRow(`SELECT body,status FROM receipts WHERE owner=12 AND id='legacy'`).Scan(&got, &status); err != nil || !bytes.Equal(got, body) || status != 201 {
		t.Fatal("failed index creation rewrote receipt", err)
	}
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 6 {
		t.Fatal("failed index creation changed format", version, err)
	}
}

func TestActiveEffectNeverSkipsItsExactRetainedRelation(t *testing.T) {
	for _, fault := range []string{"epoch", "missing", "finalized"} {
		t.Run(fault, func(t *testing.T) {
			r, err := openReceipts(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			i := cutoffIntent("live")
			_, ctx, stop, err := r.ClaimScope(context.Background(), i)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			query := `UPDATE operation_intents SET epoch='` + strings.Repeat("c", 32) + `'`
			expected := errConflict
			if fault == "missing" {
				query = `DELETE FROM operation_intents`
				expected = errInDoubt
			}
			if fault == "finalized" {
				query = `UPDATE operation_intents SET core_finalized=1`
				expected = errInDoubt
			}
			if _, err = r.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			effects := 0
			if err = operationcontrol.Effect(ctx, func() error { effects++; return nil }); !errors.Is(err, expected) || effects != 0 {
				t.Fatal("active controller skipped its retained relation", effects, err)
			}
		})
	}
}

func TestLegacyActiveReservationRequiresExactDurableInsert(t *testing.T) {
	for _, fault := range []string{"ignored", "rebound"} {
		t.Run(fault, func(t *testing.T) {
			r, err := openReceipts(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			trigger := `CREATE TRIGGER broken BEFORE INSERT ON receipts BEGIN SELECT RAISE(IGNORE); END`
			if fault == "rebound" {
				trigger = `CREATE TRIGGER broken AFTER INSERT ON receipts BEGIN UPDATE receipts SET digest='different' WHERE owner=NEW.owner AND id=NEW.id; END`
			}
			if _, err = r.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if prior, e := r.claim(12, "original", "opaque-digest"); !errors.Is(e, errInDoubt) || prior != nil {
				t.Fatal("unverified reservation acknowledged", prior, e)
			}
			var count int
			if err = r.db.QueryRow(`SELECT count(*) FROM receipts`).Scan(&count); err != nil || count != 0 || len(r.active) != 0 {
				t.Fatal("failed reservation escaped transaction", count, len(r.active), err)
			}
		})
	}
}

func TestOpenRefusesKeysOutsideHistoricalPaginationDomain(t *testing.T) {
	for _, table := range []string{"receipts", "operation_intents", "operation_helpers", "operation_cancels"} {
		for _, fault := range []string{"negative-uid", "overflow-uid", "empty-root-id"} {
			t.Run(table+"/"+fault, func(t *testing.T) {
				root := t.TempDir()
				r, err := openReceipts(root)
				if err != nil {
					t.Fatal(err)
				}
				i := cutoffIntent("historical")
				var uid int64 = int64(i.Target.UID)
				id := i.Target.RequestID
				if fault == "negative-uid" {
					uid = -1
				}
				if fault == "overflow-uid" {
					uid = 1 << 32
				}
				if fault == "empty-root-id" {
					uid = 0
					id = ""
				}
				var query string
				var args []any
				switch table {
				case "receipts":
					query = `INSERT INTO receipts(owner,id,digest,state,body,status) VALUES(?,?,?,'done',?,200)`
					args = []any{uid, id, i.Target.Digest, []byte(`{}`)}
				case "operation_intents":
					query = `INSERT INTO operation_intents(owner,id,digest,key,operation,epoch,helper_tracking,core_finalized) VALUES(?,?,?,?,?,?,1,0)`
					args = []any{uid, id, i.Target.Digest, i.Key, i.Operation, i.Epoch}
				case "operation_helpers":
					h := helperFixture(i)
					raw, e := json.Marshal(h)
					if e != nil {
						t.Fatal(e)
					}
					query = `INSERT INTO operation_helpers(owner,id,kind,key,invocation) VALUES(?,?,'bench',?,?)`
					args = []any{uid, id, i.Key, string(raw)}
				case "operation_cancels":
					query = `INSERT INTO operation_cancels(owner,id,digest,state) VALUES(?,?,?,'cancelled')`
					args = []any{uid, id, i.Target.Digest}
				}
				if _, err = r.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
				if err = r.Close(); err != nil {
					t.Fatal(err)
				}
				if reopened, e := openReceipts(root); !errors.Is(e, errInDoubt) {
					if reopened != nil {
						reopened.Close()
					}
					t.Fatal("invalid historical cursor key escaped Open", e)
				}
			})
		}
	}
}

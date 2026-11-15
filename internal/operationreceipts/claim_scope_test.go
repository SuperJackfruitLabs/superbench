package operationreceipts

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestTypedClaimCommitsControllerAuthorityBeforeCancellation(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := cutoffIntent("atomic-admission")
	cached, ctx, stop, err := r.ClaimScope(context.Background(), i)
	if err != nil || cached != nil {
		t.Fatal(cached, err)
	}
	defer stop()
	if got, ok := operationcontrol.FromContext(ctx); !ok || got != i {
		t.Fatal(got, ok)
	}
	if state, err := r.requestCancelV2(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil || state != "cancel_requested" {
		t.Fatal("typed admission invisible to cancel", state, err)
	}
	ran := false
	if err := operationcontrol.Effect(ctx, func() error { ran = true; return nil }); err == nil || ran {
		t.Fatal("effect survived cancellation", ran, err)
	}
	if snapshot, err := r.NoEffectsInventory(i.Target); err != nil || !snapshot.Typed || snapshot.Intent != i {
		t.Fatal(snapshot, err)
	}
}

func TestTypedAdmissionIgnoredInsertRollsBackWholeAdmission(t *testing.T) {
	for _, table := range []string{"receipts", "operation_intents"} {
		t.Run(table, func(t *testing.T) {
			r, err := openReceipts(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer r.db.Close()
			i := cutoffIntent("atomic-failure")
			if _, err = r.db.Exec(`CREATE TRIGGER ignored_insert BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			if _, _, stop, err := r.ClaimScope(context.Background(), i); err == nil {
				stop()
				t.Fatal("ignored intent admitted controller")
			}
			for _, table := range []string{"receipts", "operation_intents"} {
				var n int
				if err = r.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
					t.Fatal("partial admission committed", table, n, err)
				}
			}
			if len(r.active) != 0 || len(r.controllers) != 0 {
				t.Fatal("failed admission retained controller")
			}
			if _, err = r.requestCancelV2(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
				t.Fatal(err)
			}
			if _, _, stop, err := r.ClaimScope(context.Background(), i); !errors.Is(err, errCancelRequested) {
				if stop != nil {
					stop()
				}
				t.Fatal("tombstone lost admission race", err)
			}

		})
	}
}

func TestCompletedTypedClaimReplaysWithoutFreshEpoch(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := cutoffIntent("atomic-history")
	_, _, stop, err := r.ClaimScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	original := []byte(`{"historical":true}`)
	if err = r.finish(i.Target.UID, i.Target.RequestID, original, 200); err != nil {
		t.Fatal(err)
	}
	later := i
	later.Epoch = strings.Repeat("c", 32)
	cached, ctx, cancel, err := r.ClaimScope(context.Background(), later)
	if err != nil || cached == nil || ctx != nil || cancel != nil || string(cached.body) != string(original) {
		t.Fatal("historical receipt rebound", cached, ctx, err)
	}
	if stored, err := r.operationIntent(i.Target); err != nil || stored != i {
		t.Fatal(stored, err)
	}
}

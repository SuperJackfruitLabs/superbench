package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestCancellationConfirmationPreservesFinalizationCutoffAndExactEpoch(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 8, RequestID: "confirm", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	_, stop, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err = r.confirmCancellation(i); !errors.Is(err, errInDoubt) {
		t.Fatal("no durable cancel barrier", err)
	}
	if _, err = r.requestCancelV2(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	wrong := i
	wrong.Epoch = strings.Repeat("c", 32)
	if err = r.confirmCancellation(wrong); err == nil {
		t.Fatal("wrong epoch confirmed")
	}
	if err = r.confirmCancellation(i); err != nil {
		t.Fatal(err)
	}
	if state, err := r.requestCancelV2(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil || state != "cancelled" {
		t.Fatal(state, err)
	}
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); !errors.Is(err, errCancelRequested) {
		t.Fatal("cancelled request became executable", err)
	}
	var state string
	var body []byte
	if err = r.db.QueryRow(`SELECT state,body FROM receipts`).Scan(&state, &body); err != nil || state != "interrupted" || len(body) != 0 {
		t.Fatal("terminal cancellation retained growth budget", state, len(body), err)
	}
}
func TestCancellationCannotReplaceHistoricalCompletedReceipt(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 8, RequestID: "completed", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "lease", Epoch: strings.Repeat("b", 32)}
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	_, stop, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	original := []byte(`{"historical":"unchanged"}`)
	if err = r.finish(i.Target.UID, i.Target.RequestID, original, 200); err != nil {
		t.Fatal(err)
	}
	if err = r.confirmCancellation(i); !errors.Is(err, errInDoubt) {
		t.Fatal("finalized operation recancelled", err)
	}
	got, _, err := r.lookup(i.Target.UID, i.Target.RequestID, i.Target.Digest)
	if err != nil || got == nil || string(got.body) != string(original) {
		t.Fatal(got, err)
	}
}

func TestCancellationInventoryCannotChangeAcrossHelperWait(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 8, RequestID: "inventory", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "run", Epoch: strings.Repeat("b", 32)}
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	ctx, stop, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err = operationcontrol.RecordHelper(ctx, helperFixture(i)); err != nil {
		t.Fatal(err)
	}
	if _, err = r.requestCancelV2(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	expected, err := r.operationHelpers(i)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.verifyCancellationInventory(i, expected); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`DELETE FROM operation_helpers`); err != nil {
		t.Fatal(err)
	}
	if err = r.verifyCancellationInventory(i, expected); err == nil {
		t.Fatal("helper lost while waiting was certified absent")
	}
}

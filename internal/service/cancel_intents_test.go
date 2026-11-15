package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestTypedOperationIntentSurvivesRestartWithoutEpochRebind(t *testing.T) {
	root := t.TempDir()
	r, err := openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 7, RequestID: "typed", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	if _, err = r.claim(7, "typed", i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	ctx, closeScope, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer closeScope()
	got, err := r.operationIntent(i.Target)
	if err != nil || got != i {
		t.Fatal("controller created without durable typed intent", got, err)
	}
	if _, ok := operationcontrol.FromContext(ctx); !ok {
		t.Fatal("scope missing")
	}
	r.db.Close()
	r, err = openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	got, err = r.operationIntent(i.Target)
	if err != nil || got != i {
		t.Fatal("restart lost exact epoch", got, err)
	}
	if _, _, err = r.operationScope(context.Background(), i); err == nil {
		t.Fatal("interrupted operation resumed")
	}
	if state, err := r.requestCancelV2(7, "typed", i.Target.Digest); err != nil || state != "cancel_requested" {
		t.Fatal("retained typed target unavailable or claimed fenced", state, err)
	}
}
func TestPublicCancellationCannotAdoptV1OrUnboundLegacyReceipt(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	digest := strings.Repeat("c", 64)
	if _, err = r.claim(9, "v1", digest); err != nil {
		t.Fatal(err)
	}
	if state, err := r.requestCancelV2(9, "v1", digest); !errors.Is(err, errInDoubt) || state != "" {
		t.Fatal("untyped receipt adopted", state, err)
	}
	var flag string
	if err = r.db.QueryRow(`SELECT cancel_state FROM receipts WHERE owner=9 AND id='v1'`).Scan(&flag); err != nil || flag != "" {
		t.Fatal("v1 writer mutated", flag, err)
	}
	if err = r.finish(9, "v1", []byte(`{"v1":"unchanged"}`), 200); err != nil {
		t.Fatal("v1 completion blocked", err)
	}
	if state, err := r.requestCancelV2(9, "unknown", digest); err != nil || state != "cancel_requested" {
		t.Fatal("preadmission target unavailable", state, err)
	}
}
func TestIgnoredTypedIntentCannotAuthorizeController(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	if _, err = r.db.Exec(`CREATE TRIGGER ignore_intent BEFORE INSERT ON operation_intents BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 1, RequestID: "ignored", Digest: strings.Repeat("d", 64)}, Key: "card", Operation: "run", Epoch: strings.Repeat("e", 32)}
	if _, err = r.claim(1, "ignored", i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.operationScope(context.Background(), i); !errors.Is(err, errInDoubt) {
		t.Fatal("missing typed intent acknowledged", err)
	}
}

func TestMissingTypedReceiptNeverReopensIdentityAfterRestart(t *testing.T) {
	root := t.TempDir()
	r, err := openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 8, RequestID: "missing", Digest: strings.Repeat("f", 64)}, Key: "card", Operation: "setup", Epoch: strings.Repeat("a", 32)}
	if _, err = r.claim(8, "missing", i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	_, closeScope, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	closeScope()
	if _, err = r.db.Exec(`DELETE FROM receipts WHERE owner=8 AND id='missing'`); err != nil {
		t.Fatal(err)
	}
	r.db.Close()
	r, err = openReceipts(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	if _, err = r.claim(8, "missing", i.Target.Digest); !errors.Is(err, errInDoubt) {
		t.Fatal("lost receipt redispatched", err)
	}
	if _, err = r.claim(8, "missing", strings.Repeat("b", 64)); !errors.Is(err, errConflict) {
		t.Fatal("retained identity was rebound", err)
	}
	if state, err := r.requestCancelV2(8, "missing", i.Target.Digest); !errors.Is(err, errInDoubt) || state != "" {
		t.Fatal("missing authority claimed a cancellation outcome", state, err)
	}
	got, err := r.operationIntent(i.Target)
	if err != nil || got != i {
		t.Fatal("retained authority changed", got, err)
	}
	var n int
	if err = r.db.QueryRow(`SELECT count(*) FROM receipts`).Scan(&n); err != nil || n != 0 {
		t.Fatal("failed reconciliation recreated receipt", n, err)
	}
}

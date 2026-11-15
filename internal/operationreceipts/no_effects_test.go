package operationreceipts

import (
	"context"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"strings"
	"testing"
)

func TestNoEffectTombstoneCannotAcknowledgeIgnoredConfirmation(t *testing.T) {
	state := t.TempDir()
	r, err := openReceipts(state)
	if err != nil {
		t.Fatal(err)
	}
	target := cutoffIntent("before-admission").Target
	if _, err = r.requestCancelV2(target.UID, target.RequestID, target.Digest); err != nil {
		t.Fatal(err)
	}
	proof, err := r.NoEffectsInventory(target)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Typed {
		t.Fatal("invented typed intent")
	}
	if _, err = r.db.Exec(`CREATE TRIGGER ignore_terminal BEFORE UPDATE OF state ON operation_cancels BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if err = r.ConfirmNoEffects(proof); !errors.Is(err, errInDoubt) {
		t.Fatal("ignored terminal write acknowledged", err)
	}
	if state, err := r.requestCancelV2(target.UID, target.RequestID, target.Digest); err != nil || state != "cancel_requested" {
		t.Fatal(state, err)
	}
	if _, err = r.db.Exec(`DROP TRIGGER ignore_terminal`); err != nil {
		t.Fatal(err)
	}
	if err = r.ConfirmNoEffects(proof); err != nil {
		t.Fatal(err)
	}
	r.db.Close()
	r, err = openReceipts(state)
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	if state, err := r.requestCancelV2(target.UID, target.RequestID, target.Digest); err != nil || state != "cancelled" {
		t.Fatal(state, err)
	}
	if _, err = r.claim(target.UID, target.RequestID, target.Digest); !errors.Is(err, errCancelRequested) {
		t.Fatal("cancelled target executable", err)
	}
	wrong := target
	wrong.Digest = strings.Repeat("c", 64)
	if _, err = r.NoEffectsInventory(wrong); !errors.Is(err, errConflict) {
		t.Fatal("wrong digest inherited no-effect proof", err)
	}
}

func TestNoEffectsRequiresTrackedEmptyHelperInventory(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := cutoffIntent("before-core")
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	_, stop, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err = r.requestCancelV2(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	proof, err := r.NoEffectsInventory(i.Target)
	if err != nil || !proof.Typed || proof.Intent != i {
		t.Fatal(proof, err)
	}
	if _, err = r.db.Exec(`UPDATE operation_intents SET helper_tracking=0`); err != nil {
		t.Fatal(err)
	}
	if err = r.VerifyNoEffects(proof); !errors.Is(err, errInDoubt) {
		t.Fatal("legacy zero helpers adopted", err)
	}
	if _, err = r.db.Exec(`UPDATE operation_intents SET helper_tracking=1`); err != nil {
		t.Fatal(err)
	}
	if err = r.VerifyNoEffects(proof); err != nil {
		t.Fatal(err)
	}
	// Cancelling the controller never provides terminal proof by itself.
	if err = r.ConfirmNoEffects(proof); err != nil {
		t.Fatal(err)
	}
	if _, again, err := r.operationScope(context.Background(), i); err == nil {
		again()
		t.Fatal("terminal operation scope reopened")
	}
}

func TestRecordedHelperCannotBecomeNoEffectProof(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := cutoffIntent("helper-started")
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
	if _, err = r.NoEffectsInventory(i.Target); !errors.Is(err, errInDoubt) {
		t.Fatal("helper invocation became no effect proof", err)
	}
}

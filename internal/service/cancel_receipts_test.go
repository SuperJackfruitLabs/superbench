package service

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestDurableCancelBeforeClaimPreventsDispatchAndSurvivesRestart(t *testing.T) {
	state := t.TempDir()
	r, err := openReceipts(state)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	got, err := r.requestCancel(12, "queued", digest)
	if err != nil || got != "cancel_requested" {
		t.Fatal(got, err)
	}
	if _, err = r.claim(12, "queued", digest); !errors.Is(err, errCancelRequested) {
		t.Fatal("cancelled identity dispatched", err)
	}
	if _, err = r.claim(12, "queued", strings.Repeat("b", 64)); !errors.Is(err, errConflict) {
		t.Fatal("digest changed", err)
	}
	if err = r.db.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = openReceipts(state)
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	if _, err = r.claim(12, "queued", digest); !errors.Is(err, errCancelRequested) {
		t.Fatal("restart lost barrier", err)
	}
	if _, err = r.claim(13, "queued", digest); err != nil {
		t.Fatal("other UID blocked", err)
	}
}
func TestAdmittedCancelIgnoresCapacityAndCompletedReceiptRemainsHistorical(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	digest := strings.Repeat("c", 64)
	if _, err = r.claim(4, "running", digest); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO receipts(owner,id,digest,state,body,status) SELECT 99,'full-'||x,'unused','done',X'7B7D',200 FROM n`); err != nil {
		t.Fatal(err)
	}
	state, err := r.requestCancel(4, "running", digest)
	if err != nil || state != "cancel_requested" {
		t.Fatal(state, err)
	}
	if err = r.finish(4, "running", []byte(`{"grant":"MUST-NOT-PUBLISH"}`), 200); !errors.Is(err, errCancelRequested) {
		t.Fatal("late grant committed", err)
	}
	if _, err = r.db.Exec(`INSERT INTO receipts(owner,id,digest,state,body,status) VALUES (4,'done',?,'done',?,200)`, digest, []byte(`{"old":"historical"}`)); err != nil {
		t.Fatal(err)
	}
	state, err = r.requestCancel(4, "done", digest)
	if err != nil || state != "already_completed" {
		t.Fatal(state, err)
	}
	prior, _, err := r.lookup(4, "done", digest)
	if err != nil || prior == nil || !bytes.Equal(prior.body, []byte(`{"old":"historical"}`)) {
		t.Fatal("history changed", prior, err)
	}
	if _, err = r.requestCancel(4, "done", strings.Repeat("d", 64)); !errors.Is(err, errConflict) {
		t.Fatal("wrong digest accepted", err)
	}
}
func TestCancelFailureNeverAcknowledgesOrErasesReplayProtection(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	digest := strings.Repeat("e", 64)
	if _, err = r.db.Exec(`CREATE TRIGGER refuse_cancel BEFORE INSERT ON operation_cancels BEGIN SELECT RAISE(FAIL,'fault'); END`); err != nil {
		t.Fatal(err)
	}
	if state, err := r.requestCancel(3, "unknown", digest); err == nil || state != "" {
		t.Fatal("failed persistence acknowledged", state, err)
	}
	if _, err = r.claim(3, "lost", digest); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`DELETE FROM receipts WHERE owner=3 AND id='lost'`); err != nil {
		t.Fatal(err)
	}
	if state, err := r.requestCancel(3, "lost", digest); !errors.Is(err, errInDoubt) || state != "" {
		t.Fatal("lost receipt claimed no effects", state, err)
	}
}

func TestCancelTombstoneCapacityNeverBlocksAdmittedCancellation(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	digest := strings.Repeat("f", 64)
	if _, err = r.claim(8, "admitted", digest); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1024) INSERT INTO operation_cancels(owner,id,digest,state) SELECT 99,'tomb-'||x,?,'cancel_requested' FROM n`, digest); err != nil {
		t.Fatal(err)
	}
	if state, err := r.requestCancel(8, "unknown", digest); !errors.Is(err, errCapacity) || state != "" {
		t.Fatal("capacity falsely acknowledged", state, err)
	}
	if state, err := r.requestCancel(8, "admitted", digest); err != nil || state != "cancel_requested" {
		t.Fatal("known cancellation blocked", state, err)
	}
	if _, _, err := r.lookup(8, "admitted", digest); err == nil {
		t.Fatal("barrier absent")
	}
}

func TestIgnoredCancellationWriteNeverAcknowledgesBarrier(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	digest := strings.Repeat("a", 64)
	if _, err = r.claim(4, "live", digest); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`CREATE TRIGGER ignore_cancel BEFORE UPDATE OF cancel_state ON receipts BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if state, err := r.requestCancel(4, "live", digest); err == nil || state != "" {
		t.Fatal("ignored cancellation acknowledged", state, err)
	}
	if _, err = r.db.Exec(`CREATE TRIGGER ignore_tombstone BEFORE INSERT ON operation_cancels BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if state, err := r.requestCancel(4, "unknown", digest); err == nil || state != "" {
		t.Fatal("ignored tombstone acknowledged", state, err)
	}
}

func TestConfirmedCancellationIsMonotonicAndRejectsCorruptAuthority(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	digest := strings.Repeat("b", 64)
	if _, err = r.claim(7, "confirmed", digest); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`UPDATE receipts SET cancel_state='cancelled' WHERE owner=7 AND id='confirmed'`); err != nil {
		t.Fatal(err)
	}
	if state, err := r.requestCancel(7, "confirmed", digest); err != nil || state != "cancelled" {
		t.Fatal("confirmed cancellation regressed", state, err)
	}
	var state string
	if err = r.db.QueryRow(`SELECT cancel_state FROM receipts WHERE owner=7 AND id='confirmed'`).Scan(&state); err != nil || state != "cancelled" {
		t.Fatal(state, err)
	}
	if _, err = r.db.Exec(`UPDATE receipts SET cancel_state='corrupt' WHERE owner=7 AND id='confirmed'`); err != nil {
		t.Fatal(err)
	}
	if state, err := r.requestCancel(7, "confirmed", digest); !errors.Is(err, errInDoubt) || state != "" {
		t.Fatal("corrupt cancellation authority adopted", state, err)
	}
}

package service

import (
	"bytes"
	"errors"
	"testing"
)

func TestReceiptRecoveryNeverRedispatchesInterruptedWork(t *testing.T) {
	state := t.TempDir()
	r, e := openReceipts(state)
	if e != nil {
		t.Fatal(e)
	}
	if prior, e := r.claim(10, "job", "hash"); e != nil || prior != nil {
		t.Fatal(prior, e)
	}
	if _, e := r.claim(10, "job", "hash"); !errors.Is(e, errInProgress) {
		t.Fatal(e)
	}
	if _, e := r.claim(10, "job", "changed"); !errors.Is(e, errConflict) {
		t.Fatal(e)
	}
	if _, e := r.claim(11, "job", "hash"); e != nil {
		t.Fatal("request ids not scoped to peer", e)
	}
	body := []byte(`{"ok":true}`)
	if e := r.finish(11, "job", body, 200); e != nil {
		t.Fatal(e)
	}
	if e := r.db.Close(); e != nil {
		t.Fatal(e)
	}
	r, e = openReceipts(state)
	if e != nil {
		t.Fatal(e)
	}
	defer r.db.Close()
	if _, e := r.claim(10, "job", "hash"); !errors.Is(e, errInDoubt) {
		t.Fatal("interrupted operation could repeat", e)
	}
	cached, e := r.claim(11, "job", "hash")
	if e != nil || cached == nil || !bytes.Equal(cached.body, body) || cached.status != 200 {
		t.Fatal("durable result lost", e)
	}
}
func TestReceiptOversizeRetainsUncertainOutcome(t *testing.T) {
	r, e := openReceipts(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer r.db.Close()
	if _, e := r.claim(10, "job", "hash"); e != nil {
		t.Fatal(e)
	}
	if e := r.finish(10, "job", make([]byte, (64<<10)+1), 200); !errors.Is(e, errCapacity) {
		t.Fatal(e)
	}
	if _, e := r.claim(10, "job", "hash"); !errors.Is(e, errInDoubt) {
		t.Fatal("failed persistence redispatched work", e)
	}
}

func TestReceiptMissingRunningRowCannotAcknowledgeOrRedispatch(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	if _, err := r.claim(10, "lost", "hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec("DELETE FROM receipts WHERE owner=10 AND id='lost'"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.lookup(10, "lost", "hash"); !errors.Is(err, errInDoubt) {
		t.Fatal("missing active receipt admitted", err)
	}
	if err := r.finish(10, "lost", []byte(`{"ok":true}`), 200); !errors.Is(err, errInDoubt) {
		t.Fatal("missing receipt acknowledged", err)
	}
	if _, err := r.claim(10, "lost", "hash"); !errors.Is(err, errInDoubt) {
		t.Fatal("missing receipt redispatched", err)
	}
}

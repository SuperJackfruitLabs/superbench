package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestartProgressAdvancesBeyondCompletedBatch(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	for n := 0; n < 65; n++ {
		i := cutoffIntent(fmt.Sprintf("restart-%02d", n))
		if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
			t.Fatal(err)
		}
		_, stop, e := r.operationScope(context.Background(), i)
		if e != nil {
			t.Fatal(e)
		}
		stop()
		if err = r.finish(i.Target.UID, i.Target.RequestID, []byte(`{"historical":true}`), 200); err != nil {
			t.Fatal(err)
		}
	}
	first, err := r.completedOperations()
	if err != nil || len(first) != 64 {
		t.Fatal(len(first), err)
	}
	for _, i := range first {
		if err = r.markCoreFinalized(i); err != nil {
			t.Fatal(err)
		}
	}
	next, err := r.completedOperations()
	if err != nil || len(next) != 1 || next[0].Target.RequestID != "restart-64" {
		t.Fatal(next, err)
	}
	cached, found, err := r.lookup(first[0].Target.UID, first[0].Target.RequestID, first[0].Target.Digest)
	if err != nil || !found || cached == nil || string(cached.body) != `{"historical":true}` {
		t.Fatal("progress rewrote receipt", cached, found, err)
	}
}

func TestProgressRequiresCompletedExactEpochAndReadback(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := cutoffIntent("progress")
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	_, stop, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err = r.markCoreFinalized(i); !errors.Is(err, errInDoubt) {
		t.Fatal("running epoch finalized", err)
	}
	if err = r.finish(i.Target.UID, i.Target.RequestID, []byte(`{}`), 200); err != nil {
		t.Fatal(err)
	}
	wrong := i
	wrong.Epoch = "cccccccccccccccccccccccccccccccc"
	if err = r.markCoreFinalized(wrong); err == nil {
		t.Fatal("wrong epoch finalized")
	}
	if _, err = r.db.Exec(`CREATE TRIGGER ignore_progress BEFORE UPDATE OF core_finalized ON operation_intents BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if err = r.markCoreFinalized(i); !errors.Is(err, errInDoubt) {
		t.Fatal("ignored save accepted", err)
	}
	pending, err := r.completedOperations()
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
}

func cutoffIntent(id string) operationcontrol.Intent {
	return operationcontrol.Intent{Target: operationcontrol.Target{UID: 9, RequestID: id, Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "lease", Epoch: strings.Repeat("b", 32)}
}

func TestProgressMigrationAndDowngradePreserveHistoricalResponse(t *testing.T) {
	state := t.TempDir()
	r, err := openReceipts(state)
	if err != nil {
		t.Fatal(err)
	}
	i := cutoffIntent("migration")
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	_, stop, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if err = r.finish(i.Target.UID, i.Target.RequestID, []byte(`{"original":true}`), 200); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`ALTER TABLE operation_intents DROP COLUMN core_finalized;PRAGMA user_version=5`); err != nil {
		t.Fatal(err)
	}
	r.db.Close()
	r, err = openReceipts(state)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := r.completedOperations()
	if err != nil || len(pending) != 1 || pending[0] != i {
		t.Fatal(pending, err)
	}
	if err = r.markCoreFinalized(i); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`PRAGMA user_version=5`); err != nil {
		t.Fatal(err)
	}
	r.db.Close()
	if bad, err := openReceipts(state); err == nil {
		bad.db.Close()
		t.Fatal("downgraded header adopted future progress")
	}
	db, err := sql.Open("sqlite", filepath.Join(state, "service.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v, progress int
	var body []byte
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 5 {
		t.Fatal(v, err)
	}
	if err = db.QueryRow(`SELECT core_finalized FROM operation_intents`).Scan(&progress); err != nil || progress != 1 {
		t.Fatal(progress, err)
	}
	if err = db.QueryRow(`SELECT body FROM receipts`).Scan(&body); err != nil || string(body) != `{"original":true}` {
		t.Fatal(string(body), err)
	}
}

func TestCancellationProgressFailureRollsBackTerminalReceipt(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := cutoffIntent("atomic-progress")
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
	if _, err = r.db.Exec(`CREATE TRIGGER ignore_progress BEFORE UPDATE OF core_finalized ON operation_intents BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if err = r.confirmCancellation(i); !errors.Is(err, errInDoubt) {
		t.Fatal("ignored progress committed cancellation", err)
	}
	var barrier string
	if err = r.db.QueryRow(`SELECT cancel_state FROM receipts`).Scan(&barrier); err != nil || barrier != "cancel_requested" {
		t.Fatal(barrier, err)
	}
	if _, err = r.db.Exec(`DROP TRIGGER ignore_progress`); err != nil {
		t.Fatal(err)
	}
	if err = r.confirmCancellation(i); err != nil {
		t.Fatal(err)
	}
	var progress int
	if err = r.db.QueryRow(`SELECT core_finalized FROM operation_intents`).Scan(&progress); err != nil || progress != 1 {
		t.Fatal(progress, err)
	}
}

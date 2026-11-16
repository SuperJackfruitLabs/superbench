package operationreceipts

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestMigratedTypedIntentCannotCertifyEmptyHelperInventory(t *testing.T) {
	state := t.TempDir()
	r, err := openReceipts(state)
	if err != nil {
		t.Fatal(err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 7, RequestID: "legacy-helper", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	_, stop, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	// Reproduce the actual old receipt schema and its missing tracking guarantee.
	if _, err = r.db.Exec(`DROP INDEX receipt_unresolved;DROP INDEX intent_unresolved;DROP INDEX cancel_unresolved;ALTER TABLE operation_intents DROP COLUMN core_finalized;ALTER TABLE operation_intents DROP COLUMN helper_tracking;PRAGMA user_version=4`); err != nil {
		t.Fatal(err)
	}
	r.db.Close()
	r, err = openReceipts(state)
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	if _, err = r.operationHelpers(i); !errors.Is(err, errInDoubt) {
		t.Fatal("legacy empty inventory treated as no helper effects", err)
	}
	var marker int
	if err = r.db.QueryRow(`SELECT helper_tracking FROM operation_intents`).Scan(&marker); err != nil || marker != 0 {
		t.Fatal(marker, err)
	}
}
func TestCurrentIntentPinsTrackingBeforeAnyHelperCanStart(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 7, RequestID: "new-helper", Digest: strings.Repeat("c", 64)}, Key: "card", Operation: "run", Epoch: strings.Repeat("d", 32)}
	if _, err = r.claim(i.Target.UID, i.Target.RequestID, i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	_, stop, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	got, err := r.operationHelpers(i)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if _, err = r.db.Exec(`UPDATE operation_intents SET helper_tracking=2`); err != nil {
		t.Fatal(err)
	}
	if _, err = r.operationHelpers(i); !errors.Is(err, errInDoubt) {
		t.Fatal("unknown tracking revision accepted", err)
	}
}
func TestReceiptDowngradeCannotAdoptFutureHelperTracking(t *testing.T) {
	state := t.TempDir()
	r, err := openReceipts(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`PRAGMA user_version=4`); err != nil {
		t.Fatal(err)
	}
	r.db.Close()
	if got, err := openReceipts(state); err == nil {
		got.db.Close()
		t.Fatal("downgrade adopted future helper authority")
	}
	db, err := sql.Open("sqlite", filepath.Join(state, "service.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 4 {
		t.Fatal("failed open changed schema", v, err)
	}
}

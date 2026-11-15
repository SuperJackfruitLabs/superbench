package service

import (
	"bytes"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

func TestDurableCancellationWinnerNeverExportsUncommittedGrant(t *testing.T) {
	s, create, _ := v2Fixture(t)
	v2OK(t, s, v2Operator, create)
	// Simulate a cancellation commit in the finalization window. This deliberately
	// leaves the newly acquired core owner intact: a withheld result is not a fence.
	_, err := s.receipts.db.Exec(`CREATE TRIGGER cancellation_wins BEFORE UPDATE OF state ON receipts WHEN NEW.state='done' BEGIN UPDATE receipts SET cancel_state='cancel_requested' WHERE owner=OLD.owner AND id=OLD.id; SELECT RAISE(IGNORE); END`)
	if err != nil {
		t.Fatal(err)
	}
	r := protocolv2.Request{Operation: "lease", Key: create.Key, Holder: "held", RequestID: "cancel-cutoff"}
	response, status, body := v2Call(t, s, v2Operator, r)
	if status != 503 || response.Error == nil || response.Error.Code != "operation_in_doubt" || response.Result != nil {
		t.Fatal("canceled response disclosed fresh authority", status, string(body))
	}
	rows := v2Snapshot(t, s)
	if len(rows) != 1 || rows[0].Lease == nil || rows[0].Lease.Holder != "held" {
		t.Fatal("fixture did not exercise an independently acquired owner")
	}
	db, err := statestore.Open(filepath.Join(s.config.Store.State, "bench.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	private, err := benchstate.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	token := private[create.Key].Lease.Token
	if token == "" {
		t.Fatal("fixture has no retained capability")
	}
	if bytes.Contains(body, []byte(token)) {
		t.Fatal("private token escaped cancellation cutoff")
	}
}

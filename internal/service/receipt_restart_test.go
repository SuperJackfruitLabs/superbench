package service

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

func TestReceiptLookupAfterVerifiedCancelACKRestartAndReplacedDatabaseNeverGrows(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-state-restart", true: "replacement-empty-state"}[replacement], func(t *testing.T) {
			s, original, root := v2Fixture(t)
			s.proposedCancellation = true
			s.proposedReceiptLookup = true
			s.receiptSlots = make(chan struct{}, 2)
			target, err := protocolv2.NewCancelRequest(original)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(target)
			status, body := v2Raw(t, s, v2Operator, "POST", "/v2/operations", "application/json", raw)
			ack, err := protocolv2.ParseCancelResponse(body, status, target)
			if err != nil || !ack.OK || ack.Result.State != "cancel_requested" {
				t.Fatal("successful durable ACK missing", status, string(body), err)
			}
			state := s.config.Store.State
			if replacement {
				state = filepath.Join(root, "fresh-replacement-state")
				if err = os.Mkdir(state, 0700); err != nil {
					t.Fatal(err)
				}
			}
			restarted, err := openReceipts(state)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			s.receipts = restarted
			lookup := protocolv2.ReceiptLookupRequest{Version: target.Version, Operation: "receipt_lookup", TargetRequestID: target.TargetRequestID, TargetDigest: target.TargetDigest}
			raw, _ = json.Marshal(lookup)
			for n := 0; n < 2; n++ {
				status, body = v2Raw(t, s, v2Operator, "POST", "/v2/receipts/lookup", "application/json", raw)
				reply, err := protocolv2.ParseReceiptLookupResponse(body, status, lookup)
				want := "cancel_requested"
				if replacement {
					want = "absent"
				}
				if err != nil || status != 200 || reply.Result.State != want {
					t.Fatal("lookup fabricated operation outcome", status, string(body), err)
				}
			}
			db, err := sql.Open("sqlite", filepath.Join(state, "service.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, table := range []string{"receipts", "operation_intents", "operation_helpers"} {
				var count int
				if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("read created operation evidence", table, count, err)
				}
			}
			var cancels int
			if err = db.QueryRow("SELECT count(*) FROM operation_cancels").Scan(&cancels); err != nil {
				t.Fatal(err)
			}
			wantCancels := 1
			if replacement {
				wantCancels = 0
			}
			if cancels != wantCancels {
				t.Fatal("read claimed or erased cancellation barrier", cancels)
			}
			if _, err = os.Lstat(filepath.Join(s.config.Store.Data, "benches")); !os.IsNotExist(err) {
				t.Fatal("read dispatched workspace materialization", err)
			}
		})
	}
}

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
	"path/filepath"
)

func TestVerifiedCancellationACKMakesExactOriginalReplayNonExecutingAfterRestart(t *testing.T) {
	for _, state := range []string{"preadmission", "typed-before-engine", "cancelled", "already-completed"} {
		t.Run(state, func(t *testing.T) {
			s, original, _ := v2Fixture(t)
			s.proposedCancellation = true
			target, err := protocolv2.NewCancelRequest(original)
			if err != nil {
				t.Fatal(err)
			}
			var historical []byte
			if state == "already-completed" {
				_, historical = v2OK(t, s, v2Operator, original)
				// Its delivery was not acknowledged by the caller. Only durable cutoff is
				// authoritative; the coordinator must reconcile the eventual historical grant.
			} else if state == "typed-before-engine" {
				i := operationcontrol.Intent{Target: operationcontrol.Target{UID: v2Operator, RequestID: original.RequestID, Digest: target.TargetDigest}, Key: original.Key, Operation: original.Operation, Epoch: strings.Repeat("a", 32)}
				_, _, stop, err := s.receipts.ClaimScope(context.Background(), i)
				if err != nil {
					t.Fatal(err)
				}
				defer stop()
			}
			raw, _ := json.Marshal(target)
			status, ack := v2Raw(t, s, v2Operator, "POST", "/v2/operations", "application/json", raw)
			observation, err := protocolv2.ParseCancelResponse(ack, status, target)
			if err != nil || !observation.OK || observation.Result == nil {
				t.Fatal("no verified ACK", status, string(ack), err)
			}
			if state == "cancelled" {
				if result, err := s.reconcileCancellation(context.Background(), operationcontrol.Target{UID: v2Operator, RequestID: original.RequestID, Digest: target.TargetDigest}); err != nil || result != "cancelled" {
					t.Fatal(result, err)
				}
				status, ack = v2Raw(t, s, v2Operator, "POST", "/v2/operations", "application/json", raw)
				observation, err = protocolv2.ParseCancelResponse(ack, status, target)
				if err != nil || observation.Result == nil || observation.Result.State != "cancelled" {
					t.Fatal(status, string(ack), err)
				}
			}
			counts := func() [5]int {
				t.Helper()
				var out [5]int
				for n, table := range []string{"receipts", "operation_intents"} {
					if err := receiptTestDB(t, s).QueryRow("SELECT count(*) FROM " + table).Scan(&out[n]); err != nil {
						t.Fatal(err)
					}
				}
				rows := v2Snapshot(t, s)
				out[2] = len(rows)
				db, err := s.config.Store.History("", 100)
				if err != nil {
					t.Fatal(err)
				}
				out[3] = len(db)
				core, err := statestore.Open(filepath.Join(s.config.Store.State, "bench.db"), true)
				if err != nil {
					t.Fatal(err)
				}
				if err = core.QueryRow("SELECT count(*) FROM workspace_operations").Scan(&out[4]); err != nil {
					t.Fatal(err)
				}
				core.Close()
				return out
			}
			before := counts()
			if err = s.receipts.Close(); err != nil {
				t.Fatal(err)
			}
			s.receipts, err = openReceipts(s.config.Store.State)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.receipts.Close() })
			// Replay exactly the preserved request, same authenticated UID and state.
			replay, status, body := v2Call(t, s, v2Operator, original)
			if state == "already-completed" {
				if status != 200 || !bytes.Equal(body, historical) {
					t.Fatal("historical response changed", status, string(body))
				}
			} else if status < 400 || replay.Result != nil {
				t.Fatal("cancelled target redispatched", status, string(body))
			}
			if after := counts(); after != before {
				t.Fatal("exact replay produced fresh growth", before, after)
			}
			if state != "already-completed" {
				if _, err = os.Stat(s.config.Store.Data); !os.IsNotExist(err) {
					t.Fatal("exact replay created checkout data", err)
				}
			}
		})
	}
}

func TestIgnoredCancellationInsertionNeverProducesVerifiedACK(t *testing.T) {
	s, original, _ := v2Fixture(t)
	s.proposedCancellation = true
	if _, err := receiptTestDB(t, s).Exec(`CREATE TRIGGER refuse_cancel BEFORE INSERT ON operation_cancels BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	request, err := protocolv2.NewCancelRequest(original)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request)
	status, body := v2Raw(t, s, v2Operator, "POST", "/v2/operations", "application/json", raw)
	response, err := protocolv2.ParseCancelResponse(body, status, request)
	if err != nil || response.OK || response.Result != nil || status < 400 {
		t.Fatal("failed durable intent falsely authorized exact replay", status, string(body), err)
	}
	var n int
	if err = receiptTestDB(t, s).QueryRow("SELECT count(*) FROM operation_cancels").Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	// An unknown/lost/error ACK grants no client-side permission to send the
	// original growth request. This test deliberately does not redispatch it.
}

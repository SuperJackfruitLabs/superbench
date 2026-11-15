package service

import (
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

func TestReceiptLookupHistoricalBytesAndCurrentOperatorAuthority(t *testing.T) {
	s, _, _ := v2Fixture(t)
	s.proposedReceiptLookup = true
	s.receiptSlots = make(chan struct{}, 2)
	target := protocolv2.ReceiptLookupRequest{Version: protocolv2.Version, Operation: "receipt_lookup", TargetRequestID: "historical-1", TargetDigest: strings.Repeat("a", 64)}
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	call := func(uid uint32) (int, protocolv2.ReceiptLookupResponse) {
		t.Helper()
		status, body := v2Raw(t, s, uid, "POST", "/v2/receipts/lookup", "application/json", raw)
		response, err := protocolv2.ParseReceiptLookupResponse(body, status, target)
		if err != nil {
			t.Fatalf("invalid reply: %s: %v", body, err)
		}
		return status, response
	}
	if _, err := s.receipts.Claim(v2Operator, target.TargetRequestID, target.TargetDigest); err != nil {
		t.Fatal(err)
	}
	historical := []byte(" \n{\"private\":\"original-token\"}\n ")
	if err := s.receipts.Finish(v2Operator, target.TargetRequestID, historical, 503); err != nil {
		t.Fatal(err)
	}
	status, response := call(v2Operator)
	if status != 200 || response.Result == nil || response.Result.State != "completed" || response.Result.HTTPStatus != 503 || response.Result.ResponseBody != string(historical) {
		t.Fatalf("changed historical evidence: %#v", response)
	}
	status, response = call(v2OtherOperator)
	if status != 200 || response.Result.State != "absent" {
		t.Fatalf("cross-UID disclosure: %#v", response)
	}
	delete(s.config.Operators, v2Operator)
	s.config.Readers[v2Operator] = true
	// A demoted reader must be refused even if the receipt store is unavailable;
	// this orders authorization before lookup and reveals no historical existence.
	s.receipts = nil
	status, response = call(v2Operator)
	if status != 403 || response.Result != nil || response.Error == nil || response.Error.Code != "forbidden" {
		t.Fatalf("demotion accepted: %#v", response)
	}
}

func TestReceiptLookupAbsentDoesNotClaimAndReadBudgetDoesNotQueue(t *testing.T) {
	s, _, _ := v2Fixture(t)
	s.proposedReceiptLookup = true
	s.receiptSlots = make(chan struct{}, 2)
	target := protocolv2.ReceiptLookupRequest{Version: protocolv2.Version, Operation: "receipt_lookup", TargetRequestID: "never-created", TargetDigest: strings.Repeat("b", 64)}
	raw, _ := json.Marshal(target)
	status, body := v2Raw(t, s, v2Operator, "POST", "/v2/receipts/lookup", "application/json", raw)
	response, err := protocolv2.ParseReceiptLookupResponse(body, status, target)
	if err != nil || status != 200 || response.Result.State != "absent" {
		t.Fatalf("absent lookup: %s: %v", body, err)
	}
	if _, found, err := s.receipts.Lookup(v2Operator, target.TargetRequestID, target.TargetDigest); err != nil || found {
		t.Fatal("read created evidence", found, err)
	}
	s.receiptSlots <- struct{}{}
	s.receiptSlots <- struct{}{}
	status, body = v2Raw(t, s, v2Operator, "POST", "/v2/receipts/lookup", "application/json", raw)
	if status != 503 || len(s.receiptSlots) != 2 {
		t.Fatalf("read budget changed: %d %s", status, body)
	}
}

func TestReceiptLookupDisabledAndOversizedEvidenceRemainPrivate(t *testing.T) {
	s, _, _ := v2Fixture(t)
	s.receiptSlots = make(chan struct{}, 2)
	target := protocolv2.ReceiptLookupRequest{Version: protocolv2.Version, Operation: "receipt_lookup", TargetRequestID: "oversized", TargetDigest: strings.Repeat("c", 64)}
	raw, _ := json.Marshal(target)
	status, _ := v2Raw(t, s, v2Operator, "POST", "/v2/receipts/lookup", "application/json", raw)
	if status != 404 {
		t.Fatal("disabled lookup enabled", status)
	}
	s.proposedReceiptLookup = true
	if _, err := s.receipts.Claim(v2Operator, target.TargetRequestID, target.TargetDigest); err != nil {
		t.Fatal(err)
	}
	original := []byte(strings.Repeat("x", protocolv2.MaxBody+1))
	// Simulate an oversized historical row from an older/corrupt store. Current
	// producers already refuse creating such a receipt.
	if _, err := receiptTestDB(t, s).Exec("UPDATE receipts SET state='done',body=?,status=200 WHERE owner=? AND id=?", original, v2Operator, target.TargetRequestID); err != nil {
		t.Fatal(err)
	}
	status, body := v2Raw(t, s, v2Operator, "POST", "/v2/receipts/lookup", "application/json", raw)
	response, err := protocolv2.ParseReceiptLookupResponse(body, status, target)
	if err != nil || status != 503 || response.Result != nil {
		t.Fatalf("oversized disclosure: %d %s: %v", status, body, err)
	}
	prior, found, err := s.receipts.Lookup(v2Operator, target.TargetRequestID, target.TargetDigest)
	if err != nil || !found || string(prior.Bytes()) != string(original) {
		t.Fatal("lookup altered oversized original evidence", err)
	}
}

func TestReceiptConnectionPromotionCannotConsumeNormalOrCancellationCapacity(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	classifiers, normal, reads := make(chan struct{}, 8), make(chan struct{}, 64), make(chan struct{}, 2)
	for n := 0; n < cap(normal); n++ {
		normal <- struct{}{}
	}
	classifiers <- struct{}{}
	c := &peerConn{Conn: a, slot: classifiers, normal: normal, reads: reads}
	if err := c.promoteRead(); err != nil {
		t.Fatal(err)
	}
	if len(classifiers) != 0 || len(normal) != 64 || len(reads) != 1 {
		t.Fatal("incorrect dedicated promotion")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if len(reads) != 0 || len(normal) != 64 {
		t.Fatal("incorrect close release")
	}
	if err := c.promoteRead(); err == nil {
		t.Fatal("closed connection promoted")
	}
}

func TestReceiptPromotionAndCloseRaceReleasesOnlyItsOwnReservation(t *testing.T) {
	for n := 0; n < 100; n++ {
		a, b := net.Pipe()
		classifiers, normal, reads := make(chan struct{}, 8), make(chan struct{}, 64), make(chan struct{}, 2)
		classifiers <- struct{}{}
		for i := 0; i < 64; i++ {
			normal <- struct{}{}
		}
		c := &peerConn{Conn: a, slot: classifiers, normal: normal, reads: reads}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = c.promoteRead() }()
		go func() { defer wg.Done(); _ = c.Close() }()
		wg.Wait()
		_ = b.Close()
		if len(classifiers) != 0 || len(reads) != 0 || len(normal) != 64 {
			t.Fatal("read promotion/close leaked or stole reservation", len(classifiers), len(reads), len(normal))
		}
	}
}

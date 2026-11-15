package service

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func TestPrivateCancelAdmissionIsIndependentAndPublicRouteStaysClosed(t *testing.T) {
	s, _, _ := v2Fixture(t)
	request := protocolv2.CancelRequest{Version: protocolv2.Version, Operation: "cancel", TargetRequestID: "unknown", TargetDigest: strings.Repeat("a", 64)}
	raw, _ := json.Marshal(request)
	status, _ := v2Raw(t, s, v2Operator, "POST", "/v2/operations", "application/json", raw)
	if status != 400 {
		t.Fatal("public parser enabled proposed cancellation", status)
	}
	s.proposedCancellation = true
	for n := 0; n < cap(s.slots); n++ {
		s.slots <- struct{}{}
	}
	status, body := v2Raw(t, s, v2Operator, "POST", "/v2/operations", "application/json", raw)
	response, err := protocolv2.ParseCancelResponse(body, status, request)
	if err != nil || status != 200 || response.Result == nil || response.Result.State != "cancel_requested" {
		t.Fatal("cancel admission waited for normal slots or claimed a fence", status, string(body), err)
	}
	if len(s.slots) != cap(s.slots) {
		t.Fatal("cancel used normal slot")
	}
	status, body = v2Raw(t, s, v2Reader, "POST", "/v2/operations", "application/json", raw)
	response, err = protocolv2.ParseCancelResponse(body, status, request)
	if err != nil || status != 403 || response.Result != nil {
		t.Fatal("reader admitted cancellation", status, string(body), err)
	}
}

func TestPrivateGrowthDispatchBindsEpochAndFinalizesWithoutRegrantOnCancel(t *testing.T) {
	s, request, _ := v2Fixture(t)
	s.proposedCancellation = true
	_, historical := v2OK(t, s, v2Operator, request)
	digest, err := request.Digest()
	if err != nil {
		t.Fatal(err)
	}
	target := operationcontrol.Target{UID: v2Operator, RequestID: request.RequestID, Digest: digest}
	inventory, err := s.receipts.CancellationInventory(target)
	if err != nil || inventory.State != "already_completed" {
		t.Fatal("dispatch did not persist typed authority", inventory, err)
	}
	var epoch string
	var finalized int
	if err = receiptTestDB(t, s).QueryRow("SELECT epoch,core_finalized FROM operation_intents WHERE owner=? AND id=?", target.UID, target.RequestID).Scan(&epoch, &finalized); err != nil || len(epoch) != 32 || finalized != 1 {
		t.Fatal("typed authority or finalization missing", epoch, finalized, err)
	}
	db, err := statestore.Open(filepath.Join(s.config.Store.State, "bench.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	binding, err := operationcontrol.Get(db, target)
	if err != nil || binding.Phase != "finished" {
		t.Fatal("completed operation exclusion retained", binding, err)
	}
	cancel, err := protocolv2.NewCancelRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cancel)
	status, body := v2Raw(t, s, v2Operator, "POST", "/v2/operations", "application/json", raw)
	response, err := protocolv2.ParseCancelResponse(body, status, cancel)
	if err != nil || response.Result == nil || response.Result.State != "already_completed" {
		t.Fatal(status, string(body), err)
	}
	if bytes.Contains(body, []byte("grant")) || bytes.Contains(body, []byte("root_path")) {
		t.Fatal("cancel leaked capability")
	}
	_, replayed := v2OK(t, s, v2Operator, request)
	if !bytes.Equal(replayed, historical) {
		t.Fatal("late cancel changed historical response")
	}
}

package service

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

const finiteRunBody = `{"version":"superbench.v2","operation":"run","key":"card","holder":"run:one","request_id":"run-1","arguments":["worker"],"io":{"version":"superbench.finite-io.v1","stdin_base64":"AAEC","stdout_limit_bytes":1048576}}`

func TestV2FiniteGateOffKeepsLegacyBytesAndOnRefusesBeforeReceipts(t *testing.T) {
	legacy := []byte(strings.Replace(finiteRunBody, `,"io":{"version":"superbench.finite-io.v1","stdin_base64":"AAEC","stdout_limit_bytes":1048576}`, "", 1))
	replies := map[bool][]byte{}
	for _, enabled := range []bool{false, true} {
		s, _, _ := v2Fixture(t)
		s.proposedFiniteIO = enabled
		status, body := v2Raw(t, s, v2Operator, "POST", "/v2/operations", "application/json", legacy)
		replies[enabled] = append([]byte(strconvStatus(status)), body...)
		if status, _ = v2Raw(t, s, v2Operator, "POST", "/v2/operations", protocolv2.FiniteIOContentType, []byte(finiteRunBody)); status != map[bool]int{false: 400, true: 503}[enabled] {
			t.Fatal("finite run status", enabled, status)
		}
		// Disabled, a profile parameter means nothing: the legacy body proceeds (404).
		for media, body := range map[string]string{"application/json": finiteRunBody, protocolv2.FiniteIOContentType: string(legacy), protocolv2.FiniteIOContentType + " ": strings.Replace(finiteRunBody, `"run"`, `"setup"`, 1)} {
			if status, _ = v2Raw(t, s, v2Operator, "POST", "/v2/operations", media, []byte(body)); status != 400 && !(status == 404 && !enabled && body == string(legacy)) {
				t.Fatal("profile/io disagreement accepted", enabled, media, status)
			}
		}
		oversized := strings.Replace(finiteRunBody, `"AAEC"`, `"`+strings.Repeat("A", 4<<20)+`"`, 1)
		if status, _ = v2Raw(t, s, v2Operator, "POST", "/v2/operations", protocolv2.FiniteIOContentType, []byte(oversized)); status != 413 {
			t.Fatal("oversized finite body read", enabled, status)
		}
		// Uninstalled hosts refuse the finite plan before any receipt or ownership.
		if v2ReceiptCount(t, s) != 0 || len(v2Snapshot(t, s)) != 0 {
			t.Fatal("finite refusal left durable effects", enabled)
		}
	}
	if !bytes.Equal(replies[false], replies[true]) {
		t.Fatalf("gate changed legacy reply bytes:\n%s\n%s", replies[false], replies[true])
	}
}
func strconvStatus(status int) string { b, _ := json.Marshal(status); return string(b) + " " }

func TestV2FiniteReceiptReplaysAndLooksUpOnlyUnderItsProfile(t *testing.T) {
	s, _, _ := v2Fixture(t)
	s.proposedFiniteIO, s.proposedReceiptLookup, s.receiptSlots = true, true, make(chan struct{}, 2)
	request, err := protocolv2.ParseFinite([]byte(finiteRunBody))
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := request.Digest()
	corpus, err := os.ReadFile("../protocolv2/testdata/private-execution-io.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Response struct{ Body map[string]any } `json:"response"`
	}
	if err = json.Unmarshal(corpus, &fixture); err != nil {
		t.Fatal(err)
	}
	observed, _ := finiteio.Observe(bytes.Repeat([]byte{0xff}, 1<<20), *request.IO)
	fixture.Response.Body["result"].(map[string]any)["io"] = observed
	stored, _ := json.Marshal(fixture.Response.Body)
	if _, err = protocolv2.ParseResponse(stored, 422, request); err != nil || len(stored) <= protocolv2.MaxBody {
		t.Fatal("finite fixture", len(stored), err)
	}
	if _, err = s.receipts.ClaimLimit(v2Operator, request.RequestID, digest, protocolv2.ResponseLimit(request)); err != nil {
		t.Fatal(err)
	}
	if err = s.receipts.Finish(v2Operator, request.RequestID, stored, 422); err != nil {
		t.Fatal(err)
	}
	// Exact replay returns the original bytes without dispatch or authority.
	if status, body := v2Raw(t, s, v2Operator, "POST", "/v2/operations", protocolv2.FiniteIOContentType, []byte(finiteRunBody)); status != 422 || !bytes.Equal(body, stored) {
		t.Fatal("finite receipt did not replay exactly", status, len(body))
	}
	target, _ := protocolv2.NewReceiptLookupRequest(request)
	raw, _ := json.Marshal(target)
	status, body := v2Raw(t, s, v2Operator, "POST", "/v2/receipts/lookup", protocolv2.FiniteIOContentType, raw)
	found, err := protocolv2.ParseFiniteReceiptLookupResponse(body, status, target)
	if err != nil || found.Result == nil || found.Result.State != "completed" || found.Result.ResponseBody != string(stored) {
		t.Fatal("profiled lookup lost the finite receipt", status, err)
	}
	for _, enabled := range []bool{true, false} {
		s.proposedFiniteIO = enabled
		status, body = v2Raw(t, s, v2Operator, "POST", "/v2/receipts/lookup", "application/json", raw)
		plain, err := protocolv2.ParseReceiptLookupResponse(body, status, target)
		if err != nil || status != 503 || plain.Error == nil || plain.Error.Code != "unavailable" {
			t.Fatal("unprofiled lookup disclosed or truncated a finite receipt", enabled, status, err)
		}
	}
	if status, _ = v2Raw(t, s, v2Operator, "POST", "/v2/receipts/lookup", protocolv2.FiniteIOContentType, raw); status != 503 {
		t.Fatal("disabled gate honored the lookup profile", status)
	}
}

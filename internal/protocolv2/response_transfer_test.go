package protocolv2

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestResponseTransferAvailabilityIsSeparateFromInitializationPhase(t *testing.T) {
	for _, state := range []string{"relinquishing", "transferred"} {
		response, request, status := fixtureResponse(t, "status-mixed-complete")
		w := response.Result.(*Status).Benches[1].(*WorkspaceInventory)
		w.TransferState = state
		w.ReadyForAcquisition = false
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseResponse(raw, status, request); err != nil {
			t.Fatal("valid unavailable source rejected", state, err)
		}
		if w.Phase != "ready" {
			t.Fatal("availability rewrote initialization phase")
		}
	}
	response, request, status := fixtureResponse(t, "archive-archived")
	response.Result.(*Mutation).Workspace.TransferState = "transferred"
	raw, _ := json.Marshal(response)
	if _, err := ParseResponse(raw, status, request); err != nil {
		t.Fatal("archived transferred source rejected", err)
	}
	response, _, _ = fixtureResponse(t, "status-mixed-complete")
	raw, _ = json.Marshal(response)
	if bytes.Contains(raw, []byte("transfer_state")) {
		t.Fatal("ordinary response bytes acquired an optional field")
	}
}
func TestResponseTransferStateRejectsReadinessOwnersPendingAndGrants(t *testing.T) {
	for name, change := range map[string]func(*Workspace){
		"forged-ready": func(w *Workspace) { w.ReadyForAcquisition = true },
		"unknown":      func(w *Workspace) { w.TransferState = "available" },
		"owner":        func(w *Workspace) { w.Lease = &LeaseSummary{Holder: "worker", Mode: "cooperative"} },
		"initializing": func(w *Workspace) {
			w.Phase = "initializing"
			w.Lease = &LeaseSummary{Holder: "initialization", Mode: "cooperative"}
		},
		"missing-checkpoint": func(w *Workspace) { w.Checkpoint = nil },
		"pending": func(w *Workspace) {
			w.Pending = &Pending{GenerationID: "33333333333333333333333333333333", ManifestSHA256: w.Checkpoint.ManifestSHA256, PreviousSeal: w.Checkpoint.Seal, Members: w.Checkpoint.Members}
		},
	} {
		t.Run(name, func(t *testing.T) {
			response, request, status := fixtureResponse(t, "status-mixed-complete")
			w := &response.Result.(*Status).Benches[1].(*WorkspaceInventory).Workspace
			w.TransferState = "transferred"
			w.ReadyForAcquisition = false
			change(w)
			raw, _ := json.Marshal(response)
			if _, err := ParseResponse(raw, status, request); err == nil {
				t.Fatal("invalid transfer availability accepted")
			}
		})
	}
	response, request, status := fixtureResponse(t, "lease-held-grant")
	response.Result.(*Mutation).Workspace.TransferState = "transferred"
	raw, _ := json.Marshal(response)
	if _, err := ParseResponse(raw, status, request); err == nil {
		t.Fatal("transferred source granted a writer")
	}
	response, request, status = fixtureResponse(t, "status-mixed-complete")
	w := response.Result.(*Status).Benches[1].(*WorkspaceInventory)
	w.TransferState = "transferred"
	w.ReadyForAcquisition = false
	raw, _ = json.Marshal(response)
	for _, value := range []string{`""`, `null`} {
		invalid := bytes.Replace(raw, []byte(`"transfer_state":"transferred"`), []byte(`"transfer_state":`+value), 1)
		if _, err := ParseResponse(invalid, status, request); err == nil {
			t.Fatal("null or empty transfer state accepted")
		}
	}
	response, request, status = fixtureResponse(t, "archive-archived")
	response.Result.(*Mutation).Workspace.TransferState = "relinquishing"
	raw, _ = json.Marshal(response)
	if _, err := ParseResponse(raw, status, request); err == nil {
		t.Fatal("unrelinquished source archived")
	}
}

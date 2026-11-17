package protocolv2

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
)

// finiteOwnerResponse returns the pinned owner response body, its status and
// the profiled run request it answers.
func finiteOwnerResponse(t *testing.T) (map[string]any, int, Request) {
	t.Helper()
	raw, err := os.ReadFile("testdata/private-execution-io.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Request  json.RawMessage `json:"request"`
		Response struct {
			Status int            `json:"status"`
			Body   map[string]any `json:"body"`
		} `json:"response"`
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	request, err := ParseFinite(corpus.Request)
	if err != nil {
		t.Fatal(err)
	}
	return corpus.Response.Body, corpus.Response.Status, request
}
func finiteMutated(t *testing.T, body map[string]any, change func(result map[string]any)) []byte {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var copy map[string]any
	if err = json.Unmarshal(raw, &copy); err != nil {
		t.Fatal(err)
	}
	if change != nil {
		change(copy)
	}
	if raw, err = json.Marshal(copy); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFiniteOwnerResponseBindsIOToItsProfiledRunOnly(t *testing.T) {
	body, status, request := finiteOwnerResponse(t)
	result := func(m map[string]any) map[string]any { return m["result"].(map[string]any) }
	if _, err := ParseResponse(finiteMutated(t, body, nil), status, request); err != nil {
		t.Fatal("owner finite response refused", err)
	}
	legacy := request
	legacy.IO = nil
	if _, err := ParseResponse(finiteMutated(t, body, nil), status, legacy); !errors.Is(err, ErrResponse) {
		t.Fatal("io accepted for an unprofiled request", err)
	}
	limited := request
	io := *request.IO
	io.StdoutLimitBytes = 2
	limited.IO = &io
	if _, err := ParseResponse(finiteMutated(t, body, nil), status, limited); !errors.Is(err, ErrResponse) {
		t.Fatal("observation larger than the requested bound accepted", err)
	}
	for name, change := range map[string]func(map[string]any){
		"digest":  func(m map[string]any) { result(m)["io"].(map[string]any)["stdout_sha256"] = strings.Repeat("0", 64) },
		"count":   func(m map[string]any) { result(m)["io"].(map[string]any)["stdout_bytes"] = 2 },
		"version": func(m map[string]any) { result(m)["io"].(map[string]any)["version"] = "superbench.finite-io.v2" },
		"base64":  func(m map[string]any) { result(m)["io"].(map[string]any)["stdout_base64"] = "AAEC\n" },
		"running": func(m map[string]any) { result(m)["execution"].(map[string]any)["phase"] = "running" },
		"no-exit": func(m map[string]any) { delete(result(m), "exit_code") },
		"no-execution": func(m map[string]any) {
			delete(result(m), "execution")
			delete(result(m), "exit_code")
			delete(result(m), "log_truncated")
		},
		"extra-io-key":  func(m map[string]any) { result(m)["io"].(map[string]any)["stderr_base64"] = "" },
		"in-doubt-code": func(m map[string]any) { m["error"].(map[string]any)["code"] = "operation_in_doubt" },
	} {
		responseStatus := status
		if name == "in-doubt-code" {
			responseStatus = 503
		}
		if _, err := ParseResponse(finiteMutated(t, body, change), responseStatus, request); !errors.Is(err, ErrResponse) {
			t.Fatal("invalid finite observation accepted", name, err)
		}
	}
	// The unprofiled form of the same failure stays exactly representable.
	plain := finiteMutated(t, body, func(m map[string]any) { delete(result(m), "io") })
	if _, err := ParseResponse(plain, status, legacy); err != nil {
		t.Fatal("legacy run response changed", err)
	}
	if _, err := ParseResponse(plain, status, request); err != nil {
		t.Fatal("failed profiled run without observation refused", err)
	}
}

func TestFiniteSuccessRequiresObservationAndSetupNeverCarriesIt(t *testing.T) {
	body, _, request := finiteOwnerResponse(t)
	retain := false
	request.Checkpoint = &retain
	success := func(m map[string]any) {
		m["ok"] = true
		delete(m, "error")
		m["result"].(map[string]any)["exit_code"] = 0
	}
	if _, err := ParseResponse(finiteMutated(t, body, success), 200, request); err != nil {
		t.Fatal("successful finite run refused", err)
	}
	missing := finiteMutated(t, body, func(m map[string]any) { success(m); delete(m["result"].(map[string]any), "io") })
	if _, err := ParseResponse(missing, 200, request); !errors.Is(err, ErrResponse) {
		t.Fatal("successful profiled run without observation accepted", err)
	}
	setup := Request{Version: Version, Operation: "setup", Key: "card", Holder: "run:one", RequestID: "run-1", LeaseToken: request.LeaseToken}
	withPreparation := finiteMutated(t, body, func(m map[string]any) {
		m["operation"] = "setup"
		m["result"].(map[string]any)["preparation"] = []map[string]string{{"name": "a", "status": "failed"}}
	})
	if _, err := ParseResponse(withPreparation, 422, setup); !errors.Is(err, ErrResponse) {
		t.Fatal("setup response carried finite output", err)
	}
	plainSetup := finiteMutated(t, body, func(m map[string]any) {
		m["operation"] = "setup"
		result := m["result"].(map[string]any)
		result["preparation"] = []map[string]string{{"name": "a", "status": "failed"}}
		delete(result, "io")
	})
	if _, err := ParseResponse(plainSetup, 422, setup); err != nil {
		t.Fatal("setup fixture invalid without io", err)
	}
}

func TestFiniteResponseBoundIsMaxResponseBytesOnlyForProfiledRun(t *testing.T) {
	body, _, request := finiteOwnerResponse(t)
	retain := false
	request.Checkpoint = &retain
	io := *request.IO
	io.StdoutLimitBytes = finiteio.MaxBytes
	request.IO = &io
	observed, err := finiteio.Observe(bytes.Repeat([]byte{0xff}, finiteio.MaxBytes), io)
	if err != nil {
		t.Fatal(err)
	}
	raw := finiteMutated(t, body, func(m map[string]any) {
		m["ok"] = true
		delete(m, "error")
		result := m["result"].(map[string]any)
		result["exit_code"] = 0
		result["io"] = observed
	})
	limit := ResponseLimit(request)
	expected, err := finiteio.MaxResponseBytes(io)
	if err != nil || limit != expected || len(raw) <= MaxResponse || len(raw) > limit {
		t.Fatal("finite bound", limit, expected, len(raw), err)
	}
	if _, err = ParseResponse(raw, 200, request); err != nil {
		t.Fatal("largest observation refused", err)
	}
	padded := append(bytes.Clone(raw), bytes.Repeat([]byte(" "), limit-len(raw))...)
	if _, err = ParseResponse(padded, 200, request); err != nil {
		t.Fatal("exact finite response bound refused", err)
	}
	if _, err = ParseResponse(append(padded, ' '), 200, request); !errors.Is(err, ErrResponse) {
		t.Fatal("finite response bound exceeded", err)
	}
	legacy := request
	legacy.IO = nil
	if ResponseLimit(legacy) != MaxBody {
		t.Fatal("legacy growth bound changed")
	}
	invalid := request
	invalid.IO = &finiteio.Input{Version: "unknown", StdoutLimitBytes: 1}
	setup := request
	setup.Operation = "setup"
	if ResponseLimit(invalid) != 0 || ResponseLimit(setup) != 0 || ResponseLimit(Request{Operation: "status"}) != MaxResponse {
		t.Fatal("unrepresentable finite request or read-only bound")
	}
}

func TestFiniteReceiptLookupAloneRaisesOriginalBodyBound(t *testing.T) {
	_, _, request := finiteOwnerResponse(t)
	target, err := NewReceiptLookupRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{MaxBody + 1, finiteio.MaxBody, finiteio.MaxBody + 1} {
		response := ReceiptLookupResponse{Version: Version, Operation: "receipt_lookup", OK: true, Result: &ReceiptLookupResult{TargetRequestID: target.TargetRequestID, TargetDigest: target.TargetDigest, State: "completed", HTTPStatus: 200, ResponseBody: strings.Repeat("\x01", size)}}
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseReceiptLookupResponse(raw, 200, target); !errors.Is(err, ErrResponse) {
			t.Fatal("unprofiled lookup accepted a large original body", size, err)
		}
		_, err = ParseFiniteReceiptLookupResponse(raw, 200, target)
		if (err == nil) != (size <= finiteio.MaxBody) || len(raw) > finiteio.MaxLookup {
			t.Fatal("profiled lookup bound", size, len(raw), err)
		}
		if (ValidateReceiptLookupResponseProfile(response, 200, target, true) == nil) != (size <= finiteio.MaxBody) {
			t.Fatal("profiled lookup producer validation differs", size)
		}
	}
}

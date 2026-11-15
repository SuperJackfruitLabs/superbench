package protocolv2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCancellationContractPins(t *testing.T) {
	var pin struct {
		Status, Revision, Repository string
		Files                        map[string]struct{ Local, SHA256 string }
	}
	loadCancellation(t, "cancellation-source.json", &pin)
	if pin.Repository != "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty" || pin.Revision != "d578c7e3ed0709cf7d6d38f7545edacab1691201" || !strings.Contains(pin.Status, "PROPOSED") || len(pin.Files) != 5 {
		t.Fatal("cancellation pin changed")
	}
	want := map[string]string{
		"schemas/superbench/cancel-operation.v2.json":         "0a9df7d69376ff72beb169b910da9788021996eb560b751b21c6ad14abc7f60c",
		"schemas/superbench/cancel-response.v2.json":          "bf3a59af319d7771f5fb7210d559a487d8cbe3eb04237233c32851c9b3d7a69f",
		"fixtures/superbench-api/cancellation.json":           "78d563d7a63bebe53b549986e42229f1e78d2a9bca6ddf44fbe1dfd4c02fecf9",
		"fixtures/superbench-api/source-digests.json":         "b74531ee1227408cc80a84b22de21ae777f5b5ca97e78842b507dd7479bb618f",
		"fixtures/superbench-api/cancellation-lifecycle.json": "f964ed03f383e1b54af5a8f8469d51533db10644d53915cf3e4de7e19e74c896",
	}
	for name, p := range pin.Files {
		if want[name] == "" || p.SHA256 != want[name] || p.Local != filepath.Base(name) {
			t.Fatal("canonical cancellation source differs", name)
		}
		raw, err := os.ReadFile("testdata/" + p.Local)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(raw)
		if hex.EncodeToString(h[:]) != p.SHA256 {
			t.Fatal("contract drift", name)
		}
	}
}

func TestProposedCancellationCorpus(t *testing.T) {
	type item struct {
		Name, Kind, Raw string
		Body, Request   json.RawMessage
		Status          int
	}
	// Producer-only cases intentionally remain wire-valid: bytes cannot prove a fence.
	var corpus struct {
		Valid, Invalid, Lexical []item
		ProducerOnly            []item `json:"producer_only"`
	}
	loadCancellation(t, "cancellation.json", &corpus)
	if len(corpus.Valid) != 13 || len(corpus.Invalid) != 40 || len(corpus.Lexical) != 10 || len(corpus.ProducerOnly) != 1 {
		t.Fatal("incomplete cancellation corpus")
	}
	check := func(c item) error {
		raw := c.Body
		if c.Raw != "" {
			raw = []byte(c.Raw)
		}
		if c.Kind == "request" {
			_, err := ParseCancelRequest(raw)
			return err
		}
		request := CancelRequest{Version: Version, Operation: "cancel", TargetRequestID: "run-example", TargetDigest: strings.Repeat("a", 64)}
		if len(c.Request) > 0 {
			if err := json.Unmarshal(c.Request, &request); err != nil {
				return err
			}
		}
		status := c.Status
		if status == 0 {
			status = 200
		}
		_, err := ParseCancelResponse(raw, status, request)
		return err
	}
	for _, c := range append(corpus.Valid, corpus.ProducerOnly...) {
		t.Run("valid/"+c.Name, func(t *testing.T) {
			if err := check(c); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, c := range append(corpus.Invalid, corpus.Lexical...) {
		t.Run("refuse/"+c.Name, func(t *testing.T) {
			if check(c) == nil {
				t.Fatal("invalid cancellation accepted")
			}
		})
	}
}

func TestCancellationUsesActualTypedSourceDigest(t *testing.T) {
	var fixture struct {
		Vectors []struct{ Name, Wire, Canonical, SHA256 string }
	}
	loadCancellation(t, "source-digests.json", &fixture)
	if len(fixture.Vectors) != 5 {
		t.Fatal("incomplete source vectors")
	}
	for _, v := range fixture.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			request, err := Parse([]byte(v.Wire))
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := json.Marshal(request)
			if err != nil || string(canonical) != v.Canonical {
				t.Fatal("typed canonical source changed")
			}
			target, err := NewCancelRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			if target.TargetRequestID != request.RequestID || target.TargetDigest != v.SHA256 {
				t.Fatal("cancellation target changed")
			}
		})
	}
	if _, err := NewCancelRequest(Request{Version: Version, Operation: "status"}); err == nil {
		t.Fatal("read promoted to cancellation target")
	}
}

func TestCancellationRejectsInvalidUTF8AndRawByteOverflow(t *testing.T) {
	target := CancelRequest{Version: Version, Operation: "cancel", TargetRequestID: "run-example", TargetDigest: strings.Repeat("a", 64)}
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCancelRequest(append(raw, []byte(strings.Repeat(" ", MaxBody))...)); err == nil {
		t.Fatal("oversized raw request accepted")
	}
	invalid := []byte(`{"version":"superbench.v2","operation":"cancel","ok":false,"error":{"code":"unavailable","message":"X"}}`)
	for i, b := range invalid {
		if b == 'X' {
			invalid[i] = 0xff
		}
	}
	if _, err := ParseCancelResponse(invalid, 503, target); err == nil {
		t.Fatal("invalid UTF8 error accepted")
	}
}

func loadCancellation(t *testing.T, name string, out any) {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationExtensionDoesNotWidenOrdinaryRequests(t *testing.T) {
	raw := []byte(`{"version":"superbench.v2","operation":"cancel","target_request_id":"run-example","target_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if _, err := ParseCancelRequest(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(raw); err == nil {
		t.Fatal("ordinary request parser accepted proposed extension")
	}
}

func TestCancellationResponseBoundsEncodedBytesAndUTF8Messages(t *testing.T) {
	target := CancelRequest{Version: Version, Operation: "cancel", TargetRequestID: "run-example", TargetDigest: strings.Repeat("a", 64)}
	response := CancelResponse{Version: Version, Operation: "cancel", OK: true, Result: &CancelObservation{TargetRequestID: target.TargetRequestID, TargetDigest: target.TargetDigest, State: "cancel_requested"}}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte(strings.Repeat(" ", MaxBody-len(raw)))...)
	if _, err = ParseCancelResponse(raw, 200, target); err != nil {
		t.Fatal("exact maximum rejected", err)
	}
	if _, err = ParseCancelResponse(append(raw, ' '), 200, target); err == nil {
		t.Fatal("raw response overflow accepted")
	}
	response.OK = false
	response.Result = nil
	response.Error = &APIError{Code: "unavailable", Message: strings.Repeat("é", 128)}
	if err = ValidateCancelResponse(response, 503, target); err != nil {
		t.Fatal(err)
	}
	response.Error.Message += "é"
	if err = ValidateCancelResponse(response, 503, target); err == nil {
		t.Fatal("UTF8 message byte overflow accepted")
	}
}

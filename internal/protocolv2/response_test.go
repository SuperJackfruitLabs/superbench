package protocolv2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

type responseCase struct {
	Name       string          `json:"name"`
	Status     int             `json:"status"`
	Validation string          `json:"validation"`
	Body       json.RawMessage `json:"body"`
	Raw        string          `json:"raw"`
	Expected   struct {
		Operation       string   `json:"operation"`
		RequestID       string   `json:"request_id"`
		Key             string   `json:"key"`
		Holder          string   `json:"holder"`
		LeaseToken      string   `json:"lease_token"`
		Checkpoint      *bool    `json:"checkpoint"`
		AuthorizedGrant *bool    `json:"authorized_grant"`
		VerifiedFence   *bool    `json:"verified_fence"`
		InventoryKeys   []string `json:"inventory_keys"`
	} `json:"expected"`
}

func responseCorpus(t *testing.T) ([]responseCase, []responseCase, []responseCase) {
	t.Helper()
	raw, err := os.ReadFile("testdata/workspace-responses.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Status                  string
		Valid, Invalid, Lexical []responseCase
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(corpus.Status, "proposed") {
		t.Fatal("canonical proposal status changed without contract review")
	}
	return corpus.Valid, corpus.Invalid, corpus.Lexical
}
func caseRequest(c responseCase) Request {
	return Request{Version: Version, Operation: c.Expected.Operation, RequestID: c.Expected.RequestID, Key: c.Expected.Key, Holder: c.Expected.Holder, LeaseToken: c.Expected.LeaseToken, Checkpoint: c.Expected.Checkpoint}
}
func caseAuthority(c responseCase) Authority {
	// These fixture expectations are independent of the supplied response body.
	a := Authority{RootPath: "/bounded/benches/card", InventoryKeys: c.Expected.InventoryKeys}
	if c.Expected.AuthorizedGrant != nil && *c.Expected.AuthorizedGrant {
		a.GrantToken = strings.Repeat("6", 32)
	}
	a.Fenced = c.Expected.VerifiedFence != nil && *c.Expected.VerifiedFence
	return a
}
func TestPinnedResponseCorpusClientAndProducerBoundaries(t *testing.T) {
	valid, invalid, lexical := responseCorpus(t)
	for _, c := range valid {
		t.Run("valid/"+c.Name, func(t *testing.T) {
			response, err := ParseResponse(c.Body, c.Status, caseRequest(c))
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateAuthority(response, caseAuthority(c)); err != nil {
				t.Fatal("independent fixture evidence rejected", err)
			}
			if err := ValidateResponse(response, c.Status, caseRequest(c)); err != nil {
				t.Fatal("typed encoding failed client validation", err)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var before, after any
			if json.Unmarshal(c.Body, &before) != nil || json.Unmarshal(encoded, &after) != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("DTO lost fields or invented optional output")
			}
		})
	}
	for _, c := range invalid {
		t.Run("invalid/"+c.Name, func(t *testing.T) {
			response, err := ParseResponse(c.Body, c.Status, caseRequest(c))
			if c.Validation == "producer" {
				if err != nil {
					t.Fatal("producer-only evidence was incorrectly inferred by client", err)
				}
				if ValidateAuthority(response, caseAuthority(c)) == nil {
					t.Fatal("private producer authority/completeness mismatch accepted")
				}
			} else if err == nil {
				t.Fatal("invalid wire response accepted")
			}
		})
	}
	for _, c := range lexical {
		t.Run("lexical/"+c.Name, func(t *testing.T) {
			if _, err := ParseResponse([]byte(c.Raw), 200, Request{Operation: "status"}); err == nil {
				t.Fatal("ambiguous lexical response accepted")
			}
		})
	}
}
func fixtureResponse(t *testing.T, name string) (Response, Request, int) {
	t.Helper()
	valid, _, _ := responseCorpus(t)
	for _, c := range valid {
		if c.Name == name {
			r, err := ParseResponse(c.Body, c.Status, caseRequest(c))
			if err != nil {
				t.Fatal(err)
			}
			return r, caseRequest(c), c.Status
		}
	}
	t.Fatal("missing fixture", name)
	return Response{}, Request{}, 0
}
func TestResponseLexicalAndNestedPresenceRefusals(t *testing.T) {
	base, request, status := fixtureResponse(t, "host-redacted-budget")
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, result := range []string{
		`{"managed":false,"data_free_bytes":0}`, // absent zero counter
		`{"managed":false,"data_free_bytes":0,"state_free_bytes":0,"path":"private"}`,
		`{"managed":false,"data_free_bytes":-0,"state_free_bytes":0}`,
		`{"managed":false,"data_free_bytes":1.0,"state_free_bytes":0}`,
		`{"managed":false,"data_free_bytes":1e0,"state_free_bytes":0}`,
		`{"managed":false,"data_free_bytes":9007199254740992,"state_free_bytes":0}`,
		`{"managed":false,"data_free_bytes":0,"state_free_bytes":null}`,
		`{"managed":false,"data_free_bytes":0,"state_free_bytes":0,"state_free_bytes":0}`,
	} {
		t.Run(result, func(t *testing.T) {
			body["result"] = json.RawMessage(result)
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseResponse(encoded, status, request); err == nil {
				t.Fatal("invalid host observation accepted")
			}
		})
	}
	for _, bad := range [][]byte{append(append([]byte{}, raw...), raw...), bytes.Replace(raw, []byte(`"superbench.v2"`), []byte(`"\ud800"`), 1), append([]byte{0xff}, raw...)} {
		if _, err := ParseResponse(bad, status, request); err == nil {
			t.Fatal("invalid Unicode/trailing input accepted")
		}
	}
}
func TestResponsePrivacyAndRequiredNestedValues(t *testing.T) {
	response, request, status := fixtureResponse(t, "lease-held-grant")
	raw, _ := json.Marshal(response)
	for _, field := range []string{"lease_token", "continuation", "repository", "mirror", "log", "arguments", "environment", "project_id"} {
		t.Run(field, func(t *testing.T) {
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			w := body["result"].(map[string]any)["workspace"].(map[string]any)
			w[field] = "PRIVATE-CANARY"
			encoded, _ := json.Marshal(body)
			if _, err := ParseResponse(encoded, status, request); err == nil {
				t.Fatal("private/unknown workspace field accepted")
			}
		})
	}
	for _, field := range []string{"stop_requested", "mode", "holder"} {
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		lease := body["result"].(map[string]any)["workspace"].(map[string]any)["lease"].(map[string]any)
		delete(lease, field)
		encoded, _ := json.Marshal(body)
		if _, err := ParseResponse(encoded, status, request); err == nil {
			t.Fatal("missing nested lease property accepted", field)
		}
	}
}
func TestResponseIndependentGrantAndRootAuthority(t *testing.T) {
	response, request, status := fixtureResponse(t, "stop-admin-recovery-grant")
	if ValidateResponse(response, status, request) != nil {
		t.Fatal("observable eligible grant rejected")
	}
	correct := Authority{GrantToken: strings.Repeat("6", 32), Fenced: true, RootPath: "/bounded/benches/card"}
	if err := ValidateAuthority(response, correct); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Authority{{}, {GrantToken: strings.Repeat("7", 32), Fenced: true, RootPath: correct.RootPath}, {GrantToken: correct.GrantToken, RootPath: correct.RootPath}, {GrantToken: correct.GrantToken, Fenced: true, RootPath: "/another/root"}} {
		if ValidateAuthority(response, a) == nil {
			t.Fatal("independent authority mismatch accepted")
		}
	}
	statusResponse, _, _ := fixtureResponse(t, "status-empty")
	if ValidateAuthority(statusResponse, Authority{InventoryKeys: []string{}}) != nil || ValidateAuthority(statusResponse, Authority{InventoryKeys: []string{"missing"}}) == nil {
		t.Fatal("known-empty and unknown inventory proof conflated")
	}
}
func TestResponseWorstCaseEscapingAndExactTransportBounds(t *testing.T) {
	response, request, status := fixtureResponse(t, "run-held-grant")
	result := response.Result.(*RunOutcome)
	w := &result.Workspace
	w.MemberCount = 32
	w.Checkpoint.Members = nil
	for i := 0; i < 32; i++ {
		name := fmt.Sprintf("%02d", i) + strings.Repeat("x", 62)
		w.Checkpoint.Members = append(w.Checkpoint.Members, Commit{Name: name, Commit: strings.Repeat("a", 64)})
	}
	w.CheckpointRepository = w.Checkpoint.Members[0].Name
	w.Checkpoint.Seal = strings.Repeat("b", 64)
	w.Checkpoint.PreviousSeal = strings.Repeat("c", 64)
	w.Pending = &Pending{GenerationID: strings.Repeat("d", 32), ManifestSHA256: strings.Repeat("e", 64), PreviousSeal: w.Checkpoint.Seal, Members: append([]Commit(nil), w.Checkpoint.Members...)}
	result.RootPath = "/" + strings.Repeat("<", 4095)
	result.Grant.LeaseToken = strings.Repeat("6", 48)
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	worstCaseSize := len(raw)
	if len(raw) >= MaxBody || !bytes.Contains(raw, []byte(`\u003c`)) {
		t.Fatal("worst-case encoded growth body exceeds receipt reservation", len(raw))
	}
	if _, err := ParseResponse(raw, status, request); err != nil {
		t.Fatal("bounded worst-case growth response rejected", err)
	}
	for _, size := range []int{MaxBody, MaxBody + 1} {
		padded := append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), size-len(raw))...)
		_, err := ParseResponse(padded, status, request)
		if (err == nil) != (size == MaxBody) {
			t.Fatal("growth encoded-byte boundary incorrect", size, err)
		}
	}
	result.RootPath += "<"
	if ValidateResponse(response, status, request) == nil {
		t.Fatal("4097-byte root accepted")
	}
	result.RootPath = "/" + strings.Repeat("é", 2048)
	if ValidateResponse(response, status, request) == nil {
		t.Fatal("root byte bound treated as character bound")
	}
	host, hostRequest, hostStatus := fixtureResponse(t, "host-redacted-budget")
	raw, _ = json.Marshal(host)
	for _, size := range []int{MaxResponse, MaxResponse + 1} {
		padded := append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), size-len(raw))...)
		_, err := ParseResponse(padded, hostStatus, hostRequest)
		if (err == nil) != (size == MaxResponse) {
			t.Fatal("transport encoded-byte boundary incorrect", size, err)
		}
	}
	t.Log("worst-case escaped 32-member two-vector growth body fits64KiB", worstCaseSize)
}

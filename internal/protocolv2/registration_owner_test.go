package protocolv2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// These are wire-only owner vectors. A valid projection is never a substitute
// for the installed helper, retained owner lock or actual physical inspection.
func TestRegistrationObserveCanonicalOwnerCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/private-execution-io.json")
	if err != nil {
		t.Fatal(err)
	}
	pinRaw, err := os.ReadFile("testdata/private-execution-io.source.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		Repository, Revision, Path, SHA256, Status string
	}
	sum := sha256.Sum256(raw)
	if json.Unmarshal(pinRaw, &pin) != nil || pin.Repository != "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty" || pin.Revision != "496959ac16bc207135abb5ebb32e99339d124021" || pin.Path != "fixtures/superbench-api/private-execution-io.json" || pin.SHA256 != "611102fe3ba8454a5469b7439a77c2007615d1f7d7be9423c8cd2219cbb06912" || pin.SHA256 != hex.EncodeToString(sum[:]) || !strings.Contains(pin.Status, "PROPOSED") {
		t.Fatal("canonical proposed owner corpus changed")
	}
	var corpus struct {
		Status   string          `json:"status"`
		Request  json.RawMessage `json:"observation_request"`
		Response json.RawMessage `json:"observation_response"`
		Errors   []struct {
			Code     string `json:"code"`
			Statuses []int  `json:"statuses"`
		} `json:"observation_errors"`
	}
	if json.Unmarshal(raw, &corpus) != nil || !strings.Contains(corpus.Status, "PROPOSED") || len(corpus.Errors) != 8 {
		t.Fatal("incomplete owner observation corpus")
	}
	var expectedRequest RegistrationObserveRequest
	var expectedResponse RegistrationObserveResponse
	if json.Unmarshal(corpus.Request, &expectedRequest) != nil || json.Unmarshal(corpus.Response, &expectedResponse) != nil {
		t.Fatal("owner observation shape changed")
	}
	request, err := ParseRegistrationObserveRequest(corpus.Request)
	if err != nil || request != expectedRequest {
		t.Fatal("owner request refused or changed", err)
	}
	response, err := ParseRegistrationObserveResponse(corpus.Response, 200, request)
	if err != nil || !reflect.DeepEqual(response, expectedResponse) {
		t.Fatal("complete owner response refused or changed", err)
	}
	if err = ValidateRegistrationObserveResponse(response, 200, request); err != nil {
		t.Fatal("typed owner response lost wire meaning", err)
	}
	for _, vector := range corpus.Errors {
		t.Run(vector.Code, func(t *testing.T) {
			// The shared owner corpus supplies the closed code/status pairs, as in
			// its canonical adapter. Error text is intentionally not authority.
			expected := RegistrationObserveResponse{Version: Version, Operation: "registration_observe", Error: &APIError{Code: vector.Code, Message: "Observation unavailable."}}
			body, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			if len(vector.Statuses) == 0 {
				t.Fatal("owner error lost HTTP status pairs")
			}
			for _, status := range vector.Statuses {
				got, err := ParseRegistrationObserveResponse(body, status, request)
				if err != nil || !reflect.DeepEqual(got, expected) {
					t.Fatal("owner error/status refused or changed", status, err)
				}
			}
			if _, err := ParseRegistrationObserveResponse(body, 200, request); !errors.Is(err, ErrResponse) {
				t.Fatal("owner error accepted as HTTP success", err)
			}
		})
	}
}

func TestRegistrationObserveRefusesCleanForeignRoot(t *testing.T) {
	request, response, _ := observationFixture(t)
	for _, root := range []string{"/data/benches/other", "/data/not-benches/card", "/data/benches/card/extra"} {
		t.Run(root, func(t *testing.T) {
			response.Result.RootPath = root
			if err := ValidateRegistrationObserveResponse(response, 200, request); !errors.Is(err, ErrResponse) {
				t.Fatal("clean root for another logical allocation accepted", root, err)
			}
		})
	}
}

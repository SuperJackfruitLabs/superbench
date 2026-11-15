package protocolv2

import (
	"encoding/json"
	"testing"
)

func TestResponseFreshGrantBindsKnownRequestHolder(t *testing.T) {
	cases := []struct {
		name, fixture string
		failure       bool
	}{
		{"lease-success", "lease-held-grant", false},
		{"lease-partial-failure", "lease-held-grant", true},
		{"run-success-retained", "run-retained-success", false},
		{"run-partial-failure", "run-held-grant", false},
		{"setup-partial-failure", "setup-held-grant", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, request, status := fixtureResponse(t, tc.fixture)
			mutation := responseMutation(response.Result)
			if mutation == nil || mutation.Grant == nil || mutation.Workspace.Lease == nil || request.LeaseToken != "" {
				t.Fatal("fixture does not represent newly acquired retained authority")
			}
			request.Holder = mutation.Workspace.Lease.Holder
			if tc.failure {
				response.OK = false
				response.Error = &APIError{Code: "operation_failed", Message: "Inspect retained workspace state."}
				status = 422
			}
			if err := ValidateResponse(response, status, request); err != nil {
				t.Fatal("matching request holder refused", err)
			}
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ParseResponse(raw, status, request); err != nil {
				t.Fatal("matching wire holder refused", err)
			}
			mutation.Workspace.Lease.Holder = "another:run"
			if err = ValidateResponse(response, status, request); err == nil {
				t.Error("fresh grant exposed for a different known request holder")
			}
			raw, err = json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ParseResponse(raw, status, request); err == nil {
				t.Error("foreign holder accepted from wire response")
			}
		})
	}
}

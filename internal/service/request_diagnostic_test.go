package service

import (
	"encoding/json"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocol"
)

func TestUnixRequestShapeDiagnostics(t *testing.T) {
	c, _ := serviceFixture(t, true, false)
	startService(t, c)
	for _, operation := range []string{"checkpoint", "release"} {
		body := `{"version":"superbench.v1","operation":"` + operation + `","key":"card","lease_token":"` + strings.Repeat("a", 32) + `","request_id":"SECRET-VALUE"}`
		status, raw := rawCall(t, c, "POST", body, "application/json")
		var response protocol.Response
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		want := "unexpected field request_id for operation " + operation
		if status != 400 || response.Error == nil || response.Error.Code != "invalid_request" || response.Error.Message != want {
			t.Fatalf("wanted precise refusal %q: %d %s", want, status, raw)
		}
		if strings.Contains(string(raw), "SECRET-VALUE") {
			t.Fatal("echoed request value")
		}
	}
	for _, body := range []string{
		`{"version":"superbench.v1","operation":"status","` + strings.Repeat("PRIVATE", 1000) + `":"SECRET"}`,
		`{"version":"superbench.v1","operation":"SECRET-OPERATION"}`,
	} {
		status, raw := rawCall(t, c, "POST", body, "application/json")
		if status != 400 || len(raw) > 512 || strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "PRIVATE") {
			t.Fatalf("unsafe diagnostic: %d %s", status, raw)
		}
	}
	// The accepted shape proceeds past parsing to the ordinary ownership/lookup guard.
	status, raw := rawCall(t, c, "POST", `{"version":"superbench.v1","operation":"checkpoint","key":"card","lease_token":"`+strings.Repeat("a", 32)+`"}`, "application/json")
	if status == 400 || strings.Contains(string(raw), "invalid_request") {
		t.Fatalf("accepted shape rejected: %d %s", status, raw)
	}
}

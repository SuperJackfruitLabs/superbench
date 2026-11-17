package service

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// With fresh_payload on, the indexed helper refuses a scalar bench allocation,
// so a v1 create failed after recording its intent. It is refused up front.
func TestScalarCreateIsRefusedWhileFreshPayloadIsActive(t *testing.T) {
	s, create, _ := v2Fixture(t)
	s.config.Store.ProposedFreshPayload = true
	for _, operation := range []string{"create", "recover"} {
		body, _ := json.Marshal(map[string]string{"version": "superbench.v1", "operation": operation, "key": "legacy-3", "repository": create.Repositories[0].Repository, "branch": "bench/legacy-3", "request_id": operation + "-legacy-3"})
		status, raw := v2Raw(t, s, v2Operator, "POST", "/v1/operations", "application/json", body)
		if status != 409 || !strings.Contains(string(raw), `"code":"conflict"`) || !strings.Contains(string(raw), "fresh_payload is active; use the workspace API") {
			t.Fatalf("%s not refused: HTTP%d %s", operation, status, raw)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(s.config.Store.State, "create-*")); len(matches) != 0 || len(v2Snapshot(t, s)) != 0 {
		t.Fatal("refused create recorded an intent or a bench", matches)
	}
}

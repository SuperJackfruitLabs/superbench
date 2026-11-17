package service

import (
	"bytes"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

func captureLog(t *testing.T) *bytes.Buffer {
	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &out
}

func requireLogged(t *testing.T, out *bytes.Buffer, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out.String(), w) {
			t.Fatalf("missing %q in log %q", w, out.String())
		}
	}
}

// The pilot host's failed creates returned operation_failed and benchd logged
// nothing; the cause had to be inferred from sudo lines and the source.
func TestFailedOperationLogsItsCause(t *testing.T) {
	out := captureLog(t)
	s, create, root := v2Fixture(t)
	create.Repositories[1].Repository = filepath.Join(root, "missing-repository")
	v2Error(t, s, v2Operator, create, "operation_failed")
	requireLogged(t, out, `operation failed: api=v2 operation="create" request_id="create-1" key="card"`, "code=operation_failed cause=\"mirror")
}

// v1 failures and outcomes that could not be recorded (operation_in_doubt) log too.
func TestV1AndReceiptFailuresLogTheirCause(t *testing.T) {
	out := captureLog(t)
	s, create, _ := v2Fixture(t)
	v2OK(t, s, v2Operator, create)
	s.config.Store.ProposedFreshPayload = true
	v1 := func(id string) {
		body, _ := json.Marshal(map[string]string{"version": "superbench.v1", "operation": "create", "key": "legacy-3", "repository": create.Repositories[0].Repository, "request_id": id})
		v2Raw(t, s, v2Operator, "POST", "/v1/operations", "application/json", body)
	}
	v1("v1-refused")
	requireLogged(t, out, `api=v1 operation="create" request_id="v1-refused" key="legacy-3" phase="-" code=conflict cause="scalar create`)
	if _, err := receiptTestDB(t, s).Exec(`CREATE TRIGGER fail_completion BEFORE UPDATE OF state ON receipts WHEN NEW.state='done' BEGIN SELECT RAISE(FAIL,'injected receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	v1("v1-unrecorded")
	v2Call(t, s, v2Operator, protocolv2.Request{Operation: "lease", Key: create.Key, Holder: "owner", RequestID: "v2-unrecorded"})
	requireLogged(t, out, `api=v1 operation="create" request_id="v1-unrecorded" key="legacy-3" phase="-" code=receipt_save_failed cause="`,
		`api=v2 operation="lease" request_id="v2-unrecorded" key="card" phase="-" code=receipt_save_failed cause="`, "injected receipt failure")
}

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

const canonicalRevision = "04813138dd1dc32aef2e528fc08dd7e76cca92f5"

func TestMandatoryPinnedWorkspaceCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/workspace-operations.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Version string `json:"version"`
		Valid   []struct {
			Name string
			Body json.RawMessage
		} `json:"valid"`
		Invalid []struct {
			Name, Reason string
			Body         json.RawMessage
		} `json:"invalid"`
		Lexical []struct{ Name, Reason, Raw string } `json:"lexical"`
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Version != "superbench.workspace-operations-fixtures.v1" || len(corpus.Valid)+len(corpus.Invalid)+len(corpus.Lexical) != 82 {
		t.Fatal("mandatory canonical corpus differs", corpus.Version, len(corpus.Valid), len(corpus.Invalid), len(corpus.Lexical))
	}
	for _, sample := range corpus.Valid {
		t.Run("valid/"+sample.Name, func(t *testing.T) {
			r, err := Parse(sample.Body)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			again, err := Parse(encoded)
			if err != nil || !reflect.DeepEqual(r, again) {
				t.Fatal("round trip changed request", err)
			}
		})
	}
	for _, sample := range corpus.Invalid {
		t.Run("invalid/"+sample.Name, func(t *testing.T) {
			if _, err := Parse(sample.Body); !errors.Is(err, ErrInvalid) {
				t.Fatal("invalid canonical case accepted", sample.Reason, err)
			}
		})
	}
	for _, sample := range corpus.Lexical {
		t.Run("lexical/"+sample.Name, func(t *testing.T) {
			if _, err := Parse([]byte(sample.Raw)); err == nil {
				t.Fatal("invalid raw JSON accepted", sample.Reason)
			}
		})
	}
}

func TestPinnedSourceHashes(t *testing.T) {
	raw, err := os.ReadFile("testdata/source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Repository, Revision string
		Files                map[string]struct{ Local, SHA256 string }
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	if source.Repository != "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty" || source.Revision != canonicalRevision {
		t.Fatal("canonical workspace source differs")
	}
	want := map[string]string{"schemas/superbench/operation.v2.json": "operation.v2.json", "fixtures/superbench-api/workspace-operations.json": "workspace-operations.json"}
	hashes := map[string]string{
		"operation.v2.json":         "201abdbddcefcb8629246f38214a61ed3e3dfb59e3d851a9dfebd2e0096940a6",
		"workspace-operations.json": "a591ed9ee5f621824cdc33129fb4542d0e2af44ea43ee74bf8fda767df0ad120",
	}
	if len(source.Files) != len(want) {
		t.Fatal("source file list differs")
	}
	for path, local := range want {
		entry, ok := source.Files[path]
		if !ok || entry.Local != local || entry.SHA256 != hashes[local] {
			t.Fatal("source path differs", path)
		}
		raw, err = os.ReadFile("testdata/" + local)
		sum := sha256.Sum256(raw)
		if err != nil || hex.EncodeToString(sum[:]) != entry.SHA256 {
			t.Fatal("pinned contract bytes differ", local, err)
		}
	}
}

func TestWholeWorkspaceReconciliationAndOperationClasses(t *testing.T) {
	for _, operation := range []string{"create", "recover", "status", "host_status", "lease", "checkpoint", "release", "run", "setup", "stop", "stop_admin", "archive"} {
		if !Known(operation) {
			t.Fatal("canonical operation unknown", operation)
		}
		growth := operation == "create" || operation == "recover" || operation == "lease" || operation == "run" || operation == "setup"
		if Growth(operation) != growth || ReadOnly(operation) != (operation == "status" || operation == "host_status") {
			t.Fatal("incorrect authority classification", operation)
		}
	}
	if Known("takeover") || Growth("takeover") || ReadOnly("takeover") {
		t.Fatal("unknown operation gained authority")
	}
	for _, operation := range []string{"checkpoint", "release", "stop"} {
		raw := `{"version":"superbench.v2","operation":"` + operation + `","key":"task","lease_token":"` + strings.Repeat("0", 32) + `"}`
		if _, err := Parse([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := Parse([]byte(strings.TrimSuffix(raw, "}") + `,"request_id":"receipt"}`)); err == nil {
			t.Fatal("reconciliation requires receipt", operation)
		}
	}
}

func TestLexicalUnicodeNullAndExactKeys(t *testing.T) {
	for _, raw := range []string{
		`{"version":"superbench.v2","operation":"status","Version":"superbench.v2"}`,
		`{"version":"superbench.v2","operation":"status","operatio\u006e":"status"}`,
		`{"version":"superbench.v2","operation":"status","key":null}`,
		`{"version":"superbench.v2","operation":"run","key":"task","holder":"worker","request_id":"run","arguments":["echo",null]}`,
		`{"version":"superbench.v2","operation":"run","key":"task","holder":"worker","request_id":"run","arguments":["echo","\ud800"]}`,
		`{"version":"superbench.v2","operation":"run","key":"task","holder":"worker","request_id":"run","arguments":["echo","\udc00"]}`,
		`{"version":"superbench.v2","operation":"status"} []`,
		`[]`,
	} {
		if r, err := Parse([]byte(raw)); err == nil || !reflect.DeepEqual(r, Request{}) {
			t.Fatal("ambiguous JSON accepted or retained capability", err)
		}
	}
	for _, raw := range []string{
		`{"version":"superbench.v2","operation":"run","key":"task","holder":"worker","request_id":"run","arguments":["echo","\ud83d\ude42"]}`,
		`{"version":"superbench.v2","operation":"run","key":"task","holder":"worker","request_id":"run","arguments":["echo","\\ud800"]}`,
	} {
		if _, err := Parse([]byte(raw)); err != nil {
			t.Fatal("valid scalar or literal escape rejected", err)
		}
	}
	if _, err := Parse([]byte{0xff}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestBodyBudgetAndCanonicalDigest(t *testing.T) {
	raw := `{"version":"superbench.v2","operation":"status"}`
	exact := raw + strings.Repeat(" ", MaxBody-len(raw))
	r, err := Parse([]byte(exact))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Parse([]byte(exact + " ")); err == nil {
		t.Fatal("body budget ignored")
	}
	a, _ := r.Digest()
	reordered, err := Parse([]byte(`{"operation":"status","version":"superbench.v2"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := reordered.Digest()
	if a != b {
		t.Fatal("raw formatting changed receipt digest")
	}
	reordered.Key = "task"
	b, _ = reordered.Digest()
	if a == b {
		t.Fatal("different request reused receipt digest")
	}
}

func TestQuotaJSONNumbersAreExactAndBounded(t *testing.T) {
	base := `{"version":"superbench.v2","operation":"create","key":"task","request_id":"create","initialize":true,"checkpoint_repository":"repo","repositories":[{"name":"repo","repository":"/example/repo.git"}],"quota_bytes":`
	for _, value := range []string{"1024", "1024.0", "1.024e3", "1024000e-3", "1024." + strings.Repeat("0", 256)} {
		r, err := Parse([]byte(base + value + "}"))
		if err != nil || r.QuotaBytes == nil || *r.QuotaBytes != 1024 {
			t.Fatal("integral JSON spelling rejected", value, err)
		}
	}
	for _, value := range []string{"1024.0000000000000001", "1023.9999999999999999", "1e1000000000", "1e-1000000000", "-1024", "0", "1025", "true", `"1024"`} {
		if _, err := Parse([]byte(base + value + "}")); err == nil {
			t.Fatal("nonquota accepted", value)
		}
	}
}

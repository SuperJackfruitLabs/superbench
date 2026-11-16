package protocolv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
)

func finiteRequestFixture() []byte {
	return []byte(`{"version":"superbench.v2","operation":"run","key":"card","holder":"run:one","request_id":"run-1","lease_token":"66666666666666666666666666666666","arguments":["worker"],"io":{"version":"superbench.finite-io.v1","stdin_base64":"AAEC","stdout_limit_bytes":1048576}}`)
}
func TestFiniteIORequiresExplicitProfileAndPreservesLegacyEncoding(t *testing.T) {
	raw := finiteRequestFixture()
	if _, err := Parse(raw); !errors.Is(err, ErrInvalid) {
		t.Fatal("legacy parse accepted explicit IO", err)
	}
	for _, media := range []string{"application/json", FiniteIOContentType + "; charset=utf-8", "application/json; profile=unknown", FiniteIOContentType + "; profile=superbench.finite-io.v1"} {
		if _, err := ParseMedia(raw, media); !errors.Is(err, ErrInvalid) {
			t.Fatal("profile mismatch accepted", media, err)
		}
	}
	request, err := ParseMedia(raw, FiniteIOContentType)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil || !bytes.Equal(encoded, raw) {
		t.Fatal("typed IO bytes changed", string(encoded), err)
	}
	if _, err = ParseMedia(raw, `application/json; profile="superbench.finite-io.v1"`); err != nil {
		t.Fatal(err)
	}
	legacy := bytes.Replace(raw, []byte(`,"io":{"version":"superbench.finite-io.v1","stdin_base64":"AAEC","stdout_limit_bytes":1048576}`), nil, 1)
	parsed, err := Parse(legacy)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(parsed)
	if err != nil || !bytes.Equal(encoded, legacy) {
		t.Fatal("no-IO encoding changed", string(encoded), err)
	}
	if _, err = ParseMedia(legacy, FiniteIOContentType); !errors.Is(err, ErrInvalid) {
		t.Fatal("profile without IO accepted", err)
	}
}
func TestFiniteIOBodyBudgetAndCanonicalInputBeforeAnyConsumer(t *testing.T) {
	if MaxBody != finiteio.MaxLegacyBody {
		t.Fatal("legacy envelope budget drifted")
	}
	base := finiteRequestFixture()
	raw := bytes.Replace(base, []byte(`"AAEC"`), []byte(`"`+base64.StdEncoding.EncodeToString(make([]byte, finiteio.MaxBytes))+`"`), 1)
	if len(raw) <= MaxBody {
		t.Fatal("fixture did not cross legacy bound")
	}
	if _, err := Parse(raw); !errors.Is(err, ErrInvalid) {
		t.Fatal("legacy body bound widened", err)
	}
	if _, err := ParseMedia(raw, FiniteIOContentType); err != nil {
		t.Fatal(err)
	}
	raw = append(raw, bytes.Repeat([]byte(" "), finiteio.MaxBody-len(raw))...)
	if _, err := ParseMedia(raw, FiniteIOContentType); err != nil {
		t.Fatal("exact profiled body bound refused", err)
	}
	if _, err := ParseMedia(append(raw, ' '), FiniteIOContentType); !errors.Is(err, ErrInvalid) {
		t.Fatal("profile body overflow accepted", err)
	}
	for _, value := range []string{"YR==", "YQ", "YQ==\\n", "_w=="} {
		if _, err := ParseMedia(bytes.Replace(base, []byte("AAEC"), []byte(value), 1), FiniteIOContentType); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid finite bytes accepted", value, err)
		}
	}
}
func TestFiniteIOOwnerDigestVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/private-execution-io.json")
	if err != nil {
		t.Fatal(err)
	}
	pinRaw, err := os.ReadFile("testdata/private-execution-io.source.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		Revision string `json:"revision"`
		SHA256   string `json:"sha256"`
	}
	sum := sha256.Sum256(raw)
	if json.Unmarshal(pinRaw, &pin) != nil || pin.Revision != "496959ac16bc207135abb5ebb32e99339d124021" || pin.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("proposed owner corpus pin changed")
	}
	var corpus struct {
		Digests []struct {
			Name      string          `json:"name"`
			Body      json.RawMessage `json:"body"`
			Canonical string          `json:"canonical"`
			SHA       string          `json:"sha256"`
		} `json:"request_digests"`
	}
	if err = json.Unmarshal(raw, &corpus); err != nil || len(corpus.Digests) != 2 {
		t.Fatal("owner digest corpus", err)
	}
	for _, v := range corpus.Digests {
		t.Run(v.Name, func(t *testing.T) {
			request, err := ParseMedia(v.Body, FiniteIOContentType)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(request)
			if err != nil || string(encoded) != v.Canonical {
				t.Fatal("typed canonical owner bytes", string(encoded), err)
			}
			digest, err := request.Digest()
			if err != nil || digest != v.SHA {
				t.Fatal("owner digest mismatch", digest, err)
			}
			request.IO.StdoutLimitBytes = 1
			changed, err := request.Digest()
			if err != nil || changed == digest {
				t.Fatal("output bound absent from digest", err)
			}
		})
	}
}
func TestFiniteIOOwnerRefusalVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/private-execution-io.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Invalid []struct {
			Name string          `json:"name"`
			Body json.RawMessage `json:"body"`
		} `json:"invalid_requests"`
	}
	if err = json.Unmarshal(raw, &corpus); err != nil || len(corpus.Invalid) != 7 {
		t.Fatal("owner refusal corpus", err)
	}
	for _, vector := range corpus.Invalid {
		t.Run(vector.Name, func(t *testing.T) {
			if _, err := ParseMedia(vector.Body, FiniteIOContentType); !errors.Is(err, ErrInvalid) {
				t.Fatal("owner refusal accepted", err)
			}
		})
	}
}

func TestFiniteIOAliasesUnknownNullDuplicateAndBOMRefuse(t *testing.T) {
	raw := string(finiteRequestFixture())
	for _, mutant := range []string{
		strings.Replace(raw, `"io":`, `"IO":`, 1), strings.Replace(raw, `"io":{`, `"io":null,"other":{`, 1),
		strings.Replace(raw, `"stdout_limit_bytes":1048576`, `"stdout_limit_bytes":1,"stdout_limit_bytes":1`, 1),
		strings.Replace(raw, `"stdin_base64":"AAEC"`, `"Stdin_Base64":"AAEC"`, 1),
		strings.Replace(raw, `"operation":"run"`, `"operation":"setup"`, 1), "\ufeff" + raw,
	} {
		if _, err := ParseMedia([]byte(mutant), FiniteIOContentType); !errors.Is(err, ErrInvalid) {
			t.Fatal("ambiguous finite request accepted", mutant, err)
		}
	}
}

func TestFiniteProfileRejectsEveryDuplicateParameterSpelling(t *testing.T) {
	for _, media := range []string{
		FiniteIOContentType + "; profile=superbench.finite-io.v1",
		FiniteIOContentType + `; PROFILE="superbench.finite-io.v1"`,
		`application/json; profile="superbench.finite-io.v1"; profile="superbench.finite-io.v1"`,
		FiniteIOContentType + "; profile=unknown",
		`application/json; profile*=utf-8''superbench.finite-io.v1`,
		`application/json; profile*0*=utf-8''superbench.finite-io.v1`,
		`application/json; profile*0=superbench.finite-io.v1`,
		FiniteIOContentType + ";",
	} {
		if _, err := BodyLimit(media); !errors.Is(err, ErrInvalid) {
			t.Fatal("repeated or empty parameter accepted", media, err)
		}
	}
	for _, media := range []string{FiniteIOContentType, `application/json ; PROFILE = "superbench.finite-io.v1"`} {
		if limit, err := BodyLimit(media); err != nil || limit != finiteio.MaxBody {
			t.Fatal("single equivalent profile refused", media, limit, err)
		}
	}
	// Existing unprofiled parameter handling and its smaller budget are unchanged.
	if limit, err := BodyLimit(`application/json; note="a;b"`); err != nil || limit != MaxBody {
		t.Fatal("legacy parameter handling changed", limit, err)
	}
}

func TestFiniteProfileRefusesOtherwiseValidSetupInput(t *testing.T) {
	legacy := []byte(`{"version":"superbench.v2","operation":"setup","key":"card","holder":"run:one","request_id":"run-1"}`)
	if request, err := Parse(legacy); err != nil || request.Operation != "setup" {
		t.Fatal("otherwise-valid setup fixture refused", err)
	}
	raw := bytes.Replace(legacy, []byte(`"request_id":"run-1"`), []byte(`"request_id":"run-1","io":{"version":"superbench.finite-io.v1","stdin_base64":"AAEC","stdout_limit_bytes":1}`), 1)
	request, err := ParseMedia(raw, FiniteIOContentType)
	if !errors.Is(err, ErrInvalid) || request.Version != "" || request.IO != nil {
		t.Fatalf("otherwise-valid setup inherited finite input: request=%+v err=%v", request, err)
	}
}

package finiteio

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestFiniteInputIsCanonicalOwnedAndBounded(t *testing.T) {
	for _, size := range []int{0, 1, MaxBytes} {
		original := bytes.Repeat([]byte{255}, size)
		input := Input{Version: Version, StdinBase64: base64.StdEncoding.EncodeToString(original), StdoutLimitBytes: MaxBytes}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var parsed Input
		if err = json.Unmarshal(raw, &parsed); err != nil {
			t.Fatal(err)
		}
		got, err := parsed.Bytes()
		if err != nil || !bytes.Equal(got, original) {
			t.Fatal("finite input changed", err)
		}
		if len(got) > 0 {
			got[0] = 0
			again, err := parsed.Bytes()
			if err != nil || !bytes.Equal(again, original) {
				t.Fatal("input bytes alias", err)
			}
		}
	}
	for _, encoded := range []string{"YQ", "YR==", "YWJ=", "YQ==\n", "_w==", "@@@@", base64.StdEncoding.EncodeToString(make([]byte, MaxBytes+1))} {
		if _, err := (Input{Version: Version, StdinBase64: encoded, StdoutLimitBytes: 1}).Bytes(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted noncanonical or oversized input %d: %v", len(encoded), err)
		}
	}
}

func TestFiniteInputRefusesAliasesDuplicatesNullAndUnknownFields(t *testing.T) {
	good := `{"version":"superbench.finite-io.v1","stdin_base64":"","stdout_limit_bytes":1}`
	for _, raw := range []string{
		`null`, `[]`, `{}`,
		`{"Version":"superbench.finite-io.v1","stdin_base64":"","stdout_limit_bytes":1}`,
		`{"version":"superbench.finite-io.v1","version":"superbench.finite-io.v1","stdin_base64":"","stdout_limit_bytes":1}`,
		`{"version":"superbench.finite-io.v1","stdin_base64":null,"stdout_limit_bytes":1}`,
		`{"version":"superbench.finite-io.v1","stdin_base64":"","stdout_limit_bytes":0}`,
		fmt.Sprintf(`{"version":"superbench.finite-io.v1","stdin_base64":"","stdout_limit_bytes":%d}`, MaxBytes+1),
		`{"version":"superbench.finite-io.v1","stdin_base64":"","stdout_limit_bytes":1e0}`,
		`{"version":"superbench.finite-io.v1","stdin_base64":"","stdout_limit_bytes":1,"scope":"asserted"}`,
	} {
		var input Input
		if err := json.Unmarshal([]byte(raw), &input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("wrong strict refusal for %s: %v", raw, err)
		}
	}
	var input Input
	var syntax *json.SyntaxError
	if err := json.Unmarshal([]byte(good+`{}`), &input); !errors.As(err, &syntax) {
		t.Fatal("trailing document did not fail syntax", err)
	}
	if err := json.Unmarshal([]byte(good), &input); err != nil {
		t.Fatal("valid finite input refused", err)
	}
}

func TestFiniteOutputJoinsExactBytesCountHashAndRequestedLimit(t *testing.T) {
	input := Input{Version: Version, StdoutLimitBytes: MaxBytes}
	for _, size := range []int{0, MaxBytes} {
		original := bytes.Repeat([]byte{1}, size)
		result, err := Observe(original, input)
		if err != nil {
			t.Fatal(err)
		}
		if err = result.Validate(input); err != nil {
			t.Fatal(err)
		}
		result.StdoutBytes++
		if !errors.Is(result.Validate(input), ErrInvalid) {
			t.Fatal("changed count accepted")
		}
	}
	if _, err := Observe(make([]byte, MaxBytes+1), input); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized observation accepted", err)
	}
	result, err := Observe([]byte{1, 2}, input)
	if err != nil {
		t.Fatal(err)
	}
	input.StdoutLimitBytes = 1
	if !errors.Is(result.Validate(input), ErrInvalid) {
		t.Fatal("requested bound ignored")
	}
	input.StdoutLimitBytes = MaxBytes
	result.StdoutSHA256 = "a"
	if !errors.Is(result.Validate(input), ErrInvalid) {
		t.Fatal("changed hash accepted")
	}
}

func TestFiniteOutputDecoderIsClosedAndChecksActualBytes(t *testing.T) {
	input := Input{Version: Version, StdoutLimitBytes: MaxBytes}
	out, err := Observe([]byte{0, 1, 2}, input)
	if err != nil {
		t.Fatal(err)
	}
	good, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var parsed Output
	if err = json.Unmarshal(good, &parsed); err != nil || parsed != out {
		t.Fatal("exact observation changed", err)
	}
	for _, mutant := range [][]byte{
		bytes.Replace(good, []byte(`"stdout_bytes":3`), []byte(`"stdout_bytes":2`), 1),
		bytes.Replace(good, []byte(`"stdout_bytes":3`), []byte(`"stdout_bytes":3e0`), 1),
		bytes.Replace(good, []byte(`"stdout_base64":"AAEC"`), []byte(`"stdout_base64":null`), 1),
		bytes.Replace(good, []byte(`"stdout_base64":`), []byte(`"Stdout_Base64":`), 1),
		bytes.Replace(good, []byte(`"version":`), []byte(`"scope":"asserted","version":`), 1),
		bytes.Replace(good, []byte(`"stdout_bytes":3`), []byte(`"stdout_bytes":3,"stdout_bytes":3`), 1),
	} {
		var value Output
		if err = json.Unmarshal(mutant, &value); !errors.Is(err, ErrInvalid) {
			t.Fatalf("wrong exact output refusal: %s: %v", mutant, err)
		}
	}
}

func TestResponseRepresentabilityIncludesFullLegacyMetadataAtMaximumOutput(t *testing.T) {
	input := Input{Version: Version, StdoutLimitBytes: MaxBytes}
	bound, err := MaxResponseBytes(input)
	if err != nil || bound > MaxBody {
		t.Fatal("full metadata plus stdout cannot be represented", bound, err)
	}
	observed, err := Observe(make([]byte, MaxBytes), input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	if bound != MaxLegacyBody+len(`,"io":`)+len(encoded) {
		t.Fatal("bound omitted full output envelope", bound, len(encoded))
	}
	input.StdoutLimitBytes = MaxBytes + 1
	if _, err := MaxResponseBytes(input); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid output bound admitted", err)
	}
}

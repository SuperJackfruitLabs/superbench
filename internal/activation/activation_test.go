package activation

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAcceptsExplicitSwitchesAndDefaultsOmittedOnesToFalse(t *testing.T) {
	all := `{"version":"superbench.activation.v1","fresh_payload":true,"finite_io":true,"receipt_lookup":true,"cancellation":true,"registration_observation":true}`
	got, err := Parse([]byte(all))
	if err != nil || got != (Switches{true, true, true, true, true}) {
		t.Fatal(got, err)
	}
	got, err = Parse([]byte("{\n  \"version\": \"superbench.activation.v1\",\n  \"finite_io\": true\n}\n"))
	if err != nil || got != (Switches{FiniteIO: true}) || !got.Any() {
		t.Fatal(got, err)
	}
	got, err = Parse([]byte(`{"version":"superbench.activation.v1","fresh_payload":false,"finite_io":false,"receipt_lookup":false,"cancellation":false,"registration_observation":false}`))
	if err != nil || got != (Switches{}) || got.Any() {
		t.Fatal(got, err)
	}
	if got.String() != "fresh_payload=false finite_io=false receipt_lookup=false cancellation=false registration_observation=false" {
		t.Fatal(got.String())
	}
}

func TestParseRefusesEverythingButTheStrictSchema(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":              ``,
		"array":              `[]`,
		"no version":         `{"finite_io":true}`,
		"other version":      `{"version":"superbench.activation.v2"}`,
		"unknown key":        `{"version":"superbench.activation.v1","finite_io":true,"proposedFiniteIO":true}`,
		"case variant":       `{"version":"superbench.activation.v1","Finite_IO":true}`,
		"duplicate switch":   `{"version":"superbench.activation.v1","finite_io":false,"finite_io":true}`,
		"duplicate version":  `{"version":"superbench.activation.v1","version":"superbench.activation.v1"}`,
		"string bool":        `{"version":"superbench.activation.v1","finite_io":"true"}`,
		"number":             `{"version":"superbench.activation.v1","finite_io":1}`,
		"null":               `{"version":"superbench.activation.v1","finite_io":null}`,
		"nested":             `{"version":"superbench.activation.v1","finite_io":{"on":true}}`,
		"trailing object":    `{"version":"superbench.activation.v1"}{}`,
		"trailing garbage":   `{"version":"superbench.activation.v1"} x`,
		"unterminated":       `{"version":"superbench.activation.v1"`,
		"invalid utf8":       "{\"version\":\"superbench.activation.v1\",\"\xff\":true}",
		"oversized":          `{"version":"superbench.activation.v1"` + strings.Repeat(" ", MaxSize) + `}`,
		"version not string": `{"version":true}`,
	} {
		if got, err := Parse([]byte(raw)); !errors.Is(err, ErrActivation) || got != (Switches{}) {
			t.Errorf("%s: accepted %v %v", name, got, err)
		}
	}
}

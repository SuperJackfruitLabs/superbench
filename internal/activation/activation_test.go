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

// Each refused combination is named: removing any one rule from Check turns
// exactly its case green.
func TestCheckRefusesCombinationsThatStrandWorkspaces(t *testing.T) {
	ok := func() error { return nil }
	missing := func() error { return errors.New("no installed identity") }
	legacy := func() error { return errors.New("storage.json does not select indexed-v2") }
	for name, c := range map[string]struct {
		s                 Switches
		identity, storage func() error
		why               string
	}{
		"finite_io alone":                     {Switches{FiniteIO: true}, ok, ok, "finite_io requires fresh_payload"},
		"finite_io without fresh":             {Switches{FiniteIO: true, Cancellation: true, ReceiptLookup: true}, ok, ok, "finite_io requires fresh_payload"},
		"observation alone":                   {Switches{RegistrationObservation: true}, ok, ok, "registration_observation requires fresh_payload"},
		"observation without fresh":           {Switches{RegistrationObservation: true, Cancellation: true}, ok, ok, "registration_observation requires fresh_payload"},
		"fresh alone":                         {Switches{FreshPayload: true}, ok, ok, "fresh_payload requires cancellation"},
		"fresh without cancellation":          {Switches{FreshPayload: true, FiniteIO: true, ReceiptLookup: true, RegistrationObservation: true}, ok, ok, "fresh_payload requires cancellation"},
		"fresh without installed identity":    {Switches{FreshPayload: true, Cancellation: true}, missing, ok, "no installed identity"},
		"fresh with no identity check":        {Switches{FreshPayload: true, Cancellation: true}, nil, ok, "requires the installed service identity"},
		"all five without installed identity": {Switches{true, true, true, true, true}, missing, ok, "no installed identity"},
		// The pilot host's every Fresh create stranded at materializing for this.
		"fresh on a legacy storage policy":    {Switches{FreshPayload: true, Cancellation: true}, ok, legacy, "indexed quota operation journal: storage.json does not select indexed-v2"},
		"all five on a legacy storage policy": {Switches{true, true, true, true, true}, ok, legacy, "requires the indexed quota operation journal"},
		"fresh with no storage check":         {Switches{FreshPayload: true, Cancellation: true}, ok, nil, "requires the indexed quota operation journal"},
	} {
		if err := c.s.Check(c.identity, c.storage); !errors.Is(err, ErrActivation) || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCheckAcceptsEveryCompleteCombination(t *testing.T) {
	calls, storageCalls := 0, 0
	identity := func() error { calls++; return nil }
	storage := func() error { storageCalls++; return nil }
	for _, s := range []Switches{{}, {ReceiptLookup: true}, {Cancellation: true}, {Cancellation: true, ReceiptLookup: true},
		{FreshPayload: true, Cancellation: true}, {true, true, true, true, true}} {
		if err := s.Check(identity, storage); err != nil {
			t.Errorf("%v: %v", s, err)
		}
	}
	// Only fresh_payload needs the installed identity and the indexed journal.
	if calls != 2 || storageCalls != 2 {
		t.Fatalf("identity resolved %d times and storage %d times, want 2 each", calls, storageCalls)
	}
}

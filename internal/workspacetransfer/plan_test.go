package workspacetransfer_test

import (
	"bytes"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

func TestPlanParserPreservesBoundedCanonicalAuthorityAndRejectsAmbiguity(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	raw, err := p.Bytes()
	must(t, err)
	parsed, err := workspacetransfer.ParsePlan(raw)
	must(t, err)
	again, err := parsed.Bytes()
	must(t, err)
	if !bytes.Equal(raw, again) {
		t.Fatal("reviewed plan did not roundtrip exactly")
	}
	for name, invalid := range map[string][]byte{
		"unknown":          bytes.Replace(raw, []byte(`"Version":`), []byte(`"unknown":1,"Version":`), 1),
		"duplicate":        bytes.Replace(raw, []byte(`"ID":"transfer1"`), []byte(`"ID":"transfer1","ID":"transfer1"`), 1),
		"duplicate-nested": bytes.Replace(raw, []byte(`"Name":"a"`), []byte(`"Name":"a","Name":"a"`), 1),
		"null":             []byte("null"), "suffix": append(append([]byte(nil), raw...), []byte(`{}`)...),
		"invalid-utf8": append([]byte{0xff}, raw...),
		"surrogate":    bytes.Replace(raw, []byte(`"source"`), []byte(`"\ud800"`), 1),
		"wrong-digest": bytes.Replace(raw, []byte(p.Digest), []byte(strings.Repeat("f", 64)), 1),
		"oversized":    bytes.Repeat([]byte(" "), reftransfer.MaxPlan+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := workspacetransfer.ParsePlan(invalid); err == nil {
				t.Fatal("ambiguous or invalid plan accepted")
			}
		})
	}
}

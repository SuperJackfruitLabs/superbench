package protocolv2

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type reclaimCase struct {
	responseCase
	Kind string `json:"kind"`
}

// The pinned reclaim vectors: every valid request and response parses with
// the producer's own codec, every invalid or lexically ambiguous one refuses,
// and every lifecycle step names a pinned case.
func TestPinnedReclaimCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/reclaim.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Status                  string
		Valid, Invalid, Lexical []reclaimCase
	}
	if err := json.Unmarshal(raw, &corpus); err != nil || !strings.Contains(corpus.Status, "proposed") {
		t.Fatal("reclaim corpus", err)
	}
	request := Request{Version: Version, Operation: "reclaim", Key: "card"}
	parse := func(c reclaimCase, body []byte) error {
		if c.Kind == "request" {
			_, err := Parse(body)
			return err
		}
		r := request
		if c.Expected.Operation != "" {
			r = caseRequest(c.responseCase)
		}
		_, err := ParseResponse(body, c.Status, r)
		return err
	}
	names := map[string]bool{}
	for _, c := range corpus.Valid {
		names[c.Name] = true
		if err := parse(c, c.Body); err != nil {
			t.Errorf("valid/%s: %v", c.Name, err)
		}
	}
	for _, c := range corpus.Invalid {
		if c.Validation != "producer" && parse(c, c.Body) == nil {
			t.Errorf("invalid/%s accepted", c.Name)
		}
	}
	for _, c := range corpus.Lexical {
		if c.Kind == "" {
			c.Kind = map[bool]string{true: "request", false: "response"}[strings.HasPrefix(c.Name, "request-")]
		}
		if c.Status == 0 {
			c.Status = 200
		}
		if parse(c, []byte(c.Raw)) == nil {
			t.Errorf("lexical/%s accepted", c.Name)
		}
	}
	raw, err = os.ReadFile("testdata/reclaim-lifecycle.json")
	if err != nil {
		t.Fatal(err)
	}
	var lifecycle struct {
		Scenarios []struct {
			Name, Status string
			Steps        []struct {
				Request  string `json:"request_case"`
				Response string `json:"response_case"`
			}
		}
	}
	if err := json.Unmarshal(raw, &lifecycle); err != nil || len(lifecycle.Scenarios) == 0 {
		t.Fatal("reclaim lifecycle", err)
	}
	for _, s := range lifecycle.Scenarios {
		for _, step := range s.Steps {
			if step.Request != "" && !names[step.Request] || step.Response != "" && !names[step.Response] {
				t.Errorf("%s names an unpinned case", s.Name)
			}
		}
	}
}

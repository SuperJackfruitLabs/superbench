//go:build linux

package freshrecovery

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCleanRequestsCarryOnlyTheSpecAndThePlanDigest(t *testing.T) {
	s := testSpec("api")
	sha := strings.Repeat("a", 64)
	raw, err := json.Marshal(struct {
		Spec       any
		PlanSHA256 string
	}{s, sha})
	if err != nil {
		t.Fatal(err)
	}
	got, gotSHA, err := ParseClean(raw)
	if err != nil || got.Key != s.Key || gotSHA != sha {
		t.Fatalf("canonical clean request refused: %v", err)
	}
	for name, bad := range map[string]string{
		"uppercase digest": strings.Replace(string(raw), sha, strings.Repeat("A", 64), 1),
		"short digest":     strings.Replace(string(raw), sha, sha[:63], 1),
		"extra field":      strings.TrimSuffix(string(raw), "}") + `,"Plan":{}}`,
		"no digest":        strings.Replace(string(raw), `,"PlanSHA256":"`+sha+`"`, "", 1),
		"reordered":        `{"PlanSHA256":"` + sha + `",` + strings.TrimPrefix(strings.Replace(string(raw), `,"PlanSHA256":"`+sha+`"`, "", 1), "{"),
	} {
		if _, _, err := ParseClean([]byte(bad)); !errors.Is(err, ErrRecovery) {
			t.Errorf("%s clean request admitted: %v", name, err)
		}
	}
}

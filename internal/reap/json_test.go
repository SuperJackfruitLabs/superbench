package reap

import "testing"

func TestPlanJSONCannotHideDuplicateAuthorityOrNestedEntries(t *testing.T) {
	for _, raw := range []string{
		`{"Digest":"reviewed","Digest":"other"}`,
		`{"Tree":{"Entries":[],"Entries":[{"Path":"unreviewed"}]}}`,
		`{} {}`,
		string([]byte{0xff}),
	} {
		if e := CheckJSON([]byte(raw)); e == nil {
			t.Fatal("ambiguous plan accepted")
		}
	}
	if e := CheckJSON([]byte(`{"Tree":{"Entries":[{"Path":"valid","Recoverable":false}]}}`)); e != nil {
		t.Fatal(e)
	}
}

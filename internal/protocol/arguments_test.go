package protocol

import (
	"fmt"
	"testing"
)

func TestRunArgumentsRequireStringItems(t *testing.T) {
	for _, item := range []string{`null`, `true`, `false`, `0`, `1.5`, `{}`, `[]`, `["nested"]`} {
		for _, arguments := range []string{`["sh",` + item + `]`, `[` + item + `,"argument"]`} {
			t.Run(arguments, func(t *testing.T) {
				raw := []byte(fmt.Sprintf(`{"version":"superbench.v1","operation":"run","key":"k","holder":"h","request_id":"r","arguments":%s}`, arguments))
				if _, err := Parse(raw); err == nil {
					t.Fatal("non-string argument accepted")
				}
			})
		}
	}
}

func TestRunArgumentsPreserveEmptyAndLiteralStrings(t *testing.T) {
	raw := []byte(`{"version":"superbench.v1","operation":"run","key":"k","holder":"h","request_id":"r","arguments":["sh","","null"," ","\"quoted\"","雪"]}`)
	request, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sh", "", "null", " ", `"quoted"`, "雪"}
	if len(request.Arguments) != len(want) {
		t.Fatalf("arguments = %q", request.Arguments)
	}
	for i, value := range want {
		if request.Arguments[i] != value {
			t.Fatalf("argument %d = %q, want %q", i, request.Arguments[i], value)
		}
	}
}

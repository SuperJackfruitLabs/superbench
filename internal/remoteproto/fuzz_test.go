package remoteproto

import (
	"encoding/json"
	"testing"
)

func FuzzParseCredentialRequest(f *testing.F) {
	f.Add(credentialJSON(validFields()))
	f.Add([]byte(`{"version":"` + Version + `","operation":"writer_certificate","key":"card","key":"x","lease_token":"` + testToken + `","public_key":"k"}`))
	f.Add([]byte(`{"version":"` + Version + `","operation":"writer_certificate","key":"card","lease_token":"` + testToken + `","public_key":"\ud800"}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		r, err := ParseCredentialRequest(raw)
		if err != nil {
			return
		}
		again, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(again, &fields); err != nil || len(fields) != 5 {
			t.Fatal("accepted request does not re-marshal to its field set", string(again))
		}
		for _, name := range []string{"version", "operation", "key", "lease_token", "public_key"} {
			if _, ok := fields[name].(string); !ok {
				t.Fatal("missing field", name)
			}
		}
		if back, err := ParseCredentialRequest(again); err != nil || back != r {
			t.Fatal("accepted request does not round-trip", r, back, err)
		}
	})
}

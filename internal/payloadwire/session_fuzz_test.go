package payloadwire

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzParseSessionRequest: anything accepted is valid, canonical and stable.
func FuzzParseSessionRequest(f *testing.F) {
	for _, mode := range []string{"shell", "exec", "sftp"} {
		r := validSession()
		r.Mode = mode
		if mode == "exec" {
			r.Command = "echo hi"
		}
		raw, err := r.Bytes()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	payload, err := Request{Version: Version, Key: "example", LayoutID: validSession().LayoutID, PolicyDigest: validSession().PolicyDigest, Kind: "payload", Root: true, Scope: validSession().Scope, Arguments: []string{"bash"}}.Bytes()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(payload)
	f.Add([]byte(`{"Version":"superbench.payload-session.v1"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		r, err := ParseSessionRequest(raw)
		if err != nil {
			return
		}
		if r.Validate() != nil {
			t.Fatalf("parsed an invalid request: %#v", r)
		}
		canonical, err := r.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if again, err := ParseSessionRequest(canonical); err != nil || again != r {
			t.Fatalf("unstable: %v", err)
		}
		var compact bytes.Buffer
		if json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
			t.Fatalf("non-canonical bytes accepted: %q", raw)
		}
		if _, err := ParseRequest(raw); err == nil {
			t.Fatal("a session request parsed as a payload request")
		}
	})
}

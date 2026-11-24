package remoteproto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const testToken = "0123456789abcdef0123456789abcdef"

func credentialJSON(fields map[string]string) []byte {
	raw, _ := json.Marshal(fields)
	return raw
}

func validFields() map[string]string {
	return map[string]string{"version": Version, "operation": "writer_certificate", "key": "card", "lease_token": testToken, "public_key": "ssh-ed25519 AAAA comment"}
}

func TestParseCredentialRequestStrict(t *testing.T) {
	r, err := ParseCredentialRequest(credentialJSON(validFields()))
	if err != nil || r != (CredentialRequest{Version: Version, Operation: "writer_certificate", Key: "card", LeaseToken: testToken, PublicKey: "ssh-ed25519 AAAA comment"}) {
		t.Fatal("valid request refused", r, err)
	}
	if strings.Contains(r.String(), testToken) {
		t.Fatal("String shows the lease token")
	}
	refused := map[string][]byte{
		"duplicate":   []byte(`{"version":"` + Version + `","operation":"writer_certificate","key":"card","key":"card","lease_token":"` + testToken + `","public_key":"k"}`),
		"trailing":    append(credentialJSON(validFields()), []byte(` {}`)...),
		"array":       []byte(`[]`),
		"nested":      []byte(`{"version":"` + Version + `","operation":"writer_certificate","key":{},"lease_token":"` + testToken + `","public_key":"k"}`),
		"number":      []byte(`{"version":"` + Version + `","operation":"writer_certificate","key":1,"lease_token":"` + testToken + `","public_key":"k"}`),
		"oversized":   []byte(`{"version":"` + Version + `","operation":"writer_certificate","key":"card","lease_token":"` + testToken + `","public_key":"` + strings.Repeat("a", 17<<10) + `"}`),
		"not-json":    []byte(`{`),
		"empty":       nil,
		"null-values": []byte(`{"version":null,"operation":"writer_certificate","key":"card","lease_token":"` + testToken + `","public_key":"k"}`),
	}
	for name, change := range map[string]func(map[string]string){
		"unknown-field":  func(f map[string]string) { f["holder"] = "worker" },
		"wrong-version":  func(f map[string]string) { f["version"] = "superbench.v2" },
		"wrong-op":       func(f map[string]string) { f["operation"] = "remote_status" },
		"missing-key":    func(f map[string]string) { delete(f, "key") },
		"missing-token":  func(f map[string]string) { delete(f, "lease_token") },
		"missing-public": func(f map[string]string) { delete(f, "public_key") },
		"bad-key":        func(f map[string]string) { f["key"] = "-card" },
		"long-token":     func(f map[string]string) { f["lease_token"] = testToken + "01234567" },
		"upper-token":    func(f map[string]string) { f["lease_token"] = strings.ToUpper(testToken) },
		"empty-public":   func(f map[string]string) { f["public_key"] = "" },
		"multiline":      func(f map[string]string) { f["public_key"] = "ssh-ed25519 AAAA\nssh-ed25519 BBBB" },
	} {
		f := validFields()
		change(f)
		refused[name] = credentialJSON(f)
	}
	for name, raw := range refused {
		if r, err := ParseCredentialRequest(raw); !errors.Is(err, ErrInvalid) || r != (CredentialRequest{}) {
			t.Errorf("%s: accepted %v %v", name, r, err)
		}
	}
}

func TestParseStatusRequest(t *testing.T) {
	for raw, want := range map[string]StatusRequest{
		`{"version":"` + Version + `","operation":"remote_status"}`:              {Version: Version, Operation: "remote_status"},
		`{"version":"` + Version + `","operation":"remote_status","key":"card"}`: {Version: Version, Operation: "remote_status", Key: "card"},
	} {
		if got, err := ParseStatusRequest([]byte(raw)); err != nil || got != want {
			t.Errorf("%s: %v %v", raw, got, err)
		}
	}
	for _, raw := range []string{
		`{"version":"` + Version + `","operation":"remote_status","key":""}`,
		`{"version":"` + Version + `","operation":"writer_certificate"}`,
		`{"version":"` + Version + `","operation":"remote_status","lease_token":"` + testToken + `"}`,
		`{"operation":"remote_status"}`,
	} {
		if _, err := ParseStatusRequest([]byte(raw)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: accepted", raw)
		}
	}
}

func TestParseResponse(t *testing.T) {
	ok := []byte(`{"version":"` + Version + `","operation":"remote_status","ok":true,"result":{"address":"a"}}`)
	if result, err := ParseResponse(ok, 200, "remote_status"); err != nil || string(result) != `{"address":"a"}` {
		t.Fatal("ok response refused", string(result), err)
	}
	failed := []byte(`{"version":"` + Version + `","operation":"writer_certificate","ok":false,"error":{"code":"conflict","message":"lease is not usable for remote work"}}`)
	_, err := ParseResponse(failed, 409, "writer_certificate")
	var api *APIError
	if !errors.As(err, &api) || api.Code != "conflict" {
		t.Fatal("error response not returned as APIError", err)
	}
	for name, c := range map[string]struct {
		raw    []byte
		status int
		op     string
	}{
		"status-mismatch": {ok, 503, "remote_status"},
		"op-mismatch":     {ok, 200, "writer_certificate"},
		"error-on-200":    {failed, 200, "writer_certificate"},
		"unknown-field":   {[]byte(`{"version":"` + Version + `","operation":"remote_status","ok":true,"result":{},"extra":1}`), 200, "remote_status"},
		"no-result":       {[]byte(`{"version":"` + Version + `","operation":"remote_status","ok":true}`), 200, "remote_status"},
		"empty-code":      {[]byte(`{"version":"` + Version + `","operation":"remote_status","ok":false,"error":{"code":"","message":"m"}}`), 503, "remote_status"},
	} {
		if _, err := ParseResponse(c.raw, c.status, c.op); err == nil || errors.As(err, &api) {
			t.Errorf("%s: accepted as a valid reply: %v", name, err)
		}
	}
}

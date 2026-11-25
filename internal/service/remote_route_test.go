package service

import "net/http"
import "strings"
import "testing"

type fakeRemote struct{ Remote } // Route answers 200 with the service's headers; Serve is wired later.
func (fakeRemote) Route(w http.ResponseWriter, _ *http.Request, _ uint32, _ func(*http.Request) error) {
	(&Server{}).raw(w, 200, nil)
}
func TestRemoteRoutesAbsentWhenOff(t *testing.T) {
	s, _, _ := v2Fixture(t)
	off, body := v2Raw(t, s, v2Operator, "POST", "/v2/remote/credentials", "application/json", []byte(`{}`))
	s.config.Remote = fakeRemote{}
	reader, _ := v2Raw(t, s, v2Reader, "POST", "/v2/remote/status", "application/json", nil)
	if stranger, _ := v2Raw(t, s, 4242, "POST", "/v2/remote/status", "application/json", nil); off != 404 || !strings.Contains(string(body), `"not_found"`) || reader != 200 || stranger != 403 {
		t.Fatal("remote route answered while off, or not behind the peer check", off, string(body), reader, stranger)
	}
}

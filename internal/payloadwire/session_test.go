package payloadwire

import (
	"errors"
	"strings"
	"testing"
)

func validSession() SessionRequest {
	return SessionRequest{Version: SessionVersion, Key: "example", LayoutID: strings.Repeat("a", 32), PolicyDigest: strings.Repeat("b", 64), Scope: Scope{ID: strings.Repeat("c", 32), HostID: strings.Repeat("d", 64), BootID: "01234567-89ab-cdef-0123-456789abcdef"}, Mode: "shell"}
}

func TestSessionRequestValidate(t *testing.T) {
	cases := []struct {
		name string
		edit func(*SessionRequest)
		ok   bool
	}{
		{"shell", func(r *SessionRequest) {}, true},
		{"shell with term", func(r *SessionRequest) { r.Term = "xterm-256color" }, true},
		{"exec", func(r *SessionRequest) { r.Mode, r.Command = "exec", "echo hi; pwd" }, true},
		{"exec with term", func(r *SessionRequest) { r.Mode, r.Command, r.Term = "exec", "tty", "screen.linux+x_1" }, true},
		{"exec at limit", func(r *SessionRequest) { r.Mode, r.Command = "exec", strings.Repeat("x", MaxSessionCommand) }, true},
		{"sftp", func(r *SessionRequest) { r.Mode = "sftp" }, true},
		{"exec without command", func(r *SessionRequest) { r.Mode = "exec" }, false},
		{"exec over limit", func(r *SessionRequest) { r.Mode, r.Command = "exec", strings.Repeat("x", MaxSessionCommand+1) }, false},
		{"exec NUL", func(r *SessionRequest) { r.Mode, r.Command = "exec", "a\x00b" }, false},
		{"exec invalid UTF-8", func(r *SessionRequest) { r.Mode, r.Command = "exec", "a\xffb" }, false},
		{"shell with command", func(r *SessionRequest) { r.Command = "id" }, false},
		{"sftp with command", func(r *SessionRequest) { r.Mode, r.Command = "sftp", "id" }, false},
		{"sftp with term", func(r *SessionRequest) { r.Mode, r.Term = "sftp", "xterm" }, false},
		{"unknown mode", func(r *SessionRequest) { r.Mode = "git" }, false},
		{"empty mode", func(r *SessionRequest) { r.Mode = "" }, false},
		{"term space", func(r *SessionRequest) { r.Term = "x term" }, false},
		{"term newline", func(r *SessionRequest) { r.Term = "xterm\n" }, false},
		{"term too long", func(r *SessionRequest) { r.Term = strings.Repeat("x", 65) }, false},
		{"term at limit", func(r *SessionRequest) { r.Term = strings.Repeat("x", 64) }, true},
		{"payload version", func(r *SessionRequest) { r.Version = Version }, false},
		{"bad key", func(r *SessionRequest) { r.Key = "../x" }, false},
		{"bad layout", func(r *SessionRequest) { r.LayoutID = "A" }, false},
		{"bad digest", func(r *SessionRequest) { r.PolicyDigest = "b" }, false},
		{"bad scope", func(r *SessionRequest) { r.Scope.ID = "../cgroup" }, false},
		{"bad host", func(r *SessionRequest) { r.Scope.HostID = "" }, false},
		{"bad boot", func(r *SessionRequest) { r.Scope.BootID = "boot" }, false},
	}
	for _, c := range cases {
		r := validSession()
		c.edit(&r)
		err := r.Validate()
		if (err == nil) != c.ok || err != nil && !errors.Is(err, ErrRequest) {
			t.Errorf("%s: %v", c.name, err)
		}
		raw, bytesErr := r.Bytes()
		if (bytesErr == nil) != c.ok {
			t.Errorf("%s bytes: %v", c.name, bytesErr)
		}
		if c.ok {
			parsed, err := ParseSessionRequest(raw)
			if err != nil || parsed != r {
				t.Errorf("%s round trip: %v", c.name, err)
			}
		}
	}
}

func TestParseSessionRequestRefusesNonCanonical(t *testing.T) {
	raw, err := validSession().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, bad := range []string{
		strings.Replace(text, `"Mode":"shell"`, `"Mode":"shell","Extra":1`, 1),
		strings.Replace(text, `{"Version"`, `{"Mode":"exec","Version"`, 1),
		text + "x",
		"",
	} {
		if _, err := ParseSessionRequest([]byte(bad)); !errors.Is(err, ErrRequest) {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := ParseSessionRequest([]byte(" " + text + "\n")); err != nil {
		t.Errorf("whitespace refused: %v", err)
	}
}

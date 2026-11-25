package payloadwire

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// SessionVersion is the helper's second fixed mode: a remote session in the
// bench's private mount namespace. It names no executable, path, uid or policy;
// Mode selects one of the helper's fixed argvs. It is never a payload.v1 frame.
const SessionVersion = "superbench.payload-session.v1"

// MaxSessionCommand bounds an exec request's command, as the SSH server does.
const MaxSessionCommand = 64 << 10

// MaxSessionRequest bounds the encoded frame: the command after JSON escaping.
const MaxSessionRequest = 512 << 10

var sessionTerm = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)

// SessionRequest: Mode is "shell", "exec" or "sftp". A non-empty Term means the
// session's one descriptor is a PTY; otherwise it receives three pipes.
type SessionRequest struct {
	Version, Key, LayoutID, PolicyDigest string
	Scope                                Scope
	Mode, Command, Term                  string
}

func (r SessionRequest) Validate() error {
	if r.Version != SessionVersion || !logical.MatchString(r.Key) || !identity.MatchString(r.LayoutID) || !hostIdentity.MatchString(r.PolicyDigest) || !identity.MatchString(r.Scope.ID) || !hostIdentity.MatchString(r.Scope.HostID) || !bootIdentity.MatchString(r.Scope.BootID) {
		return ErrRequest
	}
	switch r.Mode {
	case "exec":
		if r.Command == "" || len(r.Command) > MaxSessionCommand || !utf8.ValidString(r.Command) || strings.ContainsRune(r.Command, 0) {
			return ErrRequest
		}
	case "shell":
		if r.Command != "" {
			return ErrRequest
		}
	case "sftp":
		if r.Command != "" || r.Term != "" {
			return ErrRequest
		}
	default:
		return ErrRequest
	}
	if r.Term != "" && !sessionTerm.MatchString(r.Term) {
		return ErrRequest
	}
	return nil
}

func (r SessionRequest) Bytes() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxSessionRequest {
		return nil, ErrRequest
	}
	return raw, nil
}

// ParseSessionRequest accepts only the canonical encoding, with optional JSON
// whitespace. Unknown, duplicate, reordered or mistyped fields refuse.
func ParseSessionRequest(raw []byte) (SessionRequest, error) {
	var r SessionRequest
	if len(raw) > MaxSessionRequest || !utf8.Valid(raw) || json.Unmarshal(raw, &r) != nil {
		return SessionRequest{}, ErrRequest
	}
	canonical, err := r.Bytes()
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return SessionRequest{}, ErrRequest
	}
	return r, nil
}

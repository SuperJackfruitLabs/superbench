package payloadexec

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

const Version = "superbench.payload.v1"

// MaxRequest bounds the new private helper envelope, including JSON escaping.
// It does not enlarge command, setup, wire or kernel execution limits.
const MaxRequest = 16 << 20

// MaxPayloadInput bounds decoded opaque stdin. Its base64 encoding also counts
// against MaxRequest; the limit is fixed here, never selected by a payload.
const MaxPayloadInput = 1 << 20

var ErrRequest = errors.New("invalid managed payload launch request")
var logical = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var identity = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hostIdentity = regexp.MustCompile(`^[a-f0-9]{64}$`)
var bootIdentity = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type Scope struct {
	ID, HostID, BootID string
}

// Request carries command input, never a caller-selected UID, privileged path,
// executable policy, cgroup root, lease token or state database location.
type Request struct {
	Version, Key, LayoutID, Member, PolicyDigest, Kind string
	Root                                               bool
	Scope                                              Scope
	Arguments                                          []string
	// Input is optional private transport data, not an execution authority.
	// Omission preserves the existing payload EOF and Git streaming encodings.
	Input []byte `json:",omitempty"`
}

func (r Request) Validate() error {
	if len(r.Input) > MaxPayloadInput || len(r.Input) != 0 && r.Kind != "payload" {
		return ErrRequest
	}
	if r.Version != Version || !hostIdentity.MatchString(r.PolicyDigest) || r.Kind != "payload" && r.Kind != "git" || r.Root && (r.Member != "" || r.Kind != "payload") || !logical.MatchString(r.Key) || !identity.MatchString(r.LayoutID) || r.Member != "" && !logical.MatchString(r.Member) || !identity.MatchString(r.Scope.ID) || !hostIdentity.MatchString(r.Scope.HostID) || !bootIdentity.MatchString(r.Scope.BootID) || len(r.Arguments) == 0 || r.Arguments[0] == "" {
		return ErrRequest
	}
	for _, argument := range r.Arguments {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) {
			return ErrRequest
		}
	}
	if r.Kind == "git" && r.Arguments[0] != "git" {
		return ErrRequest
	}
	return nil
}

func (r Request) Bytes() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxRequest {
		return nil, ErrRequest
	}
	return raw, nil
}

func ParseRequest(raw []byte) (Request, error) {
	var r Request
	if len(raw) > MaxRequest || !utf8.Valid(raw) || json.Unmarshal(raw, &r) != nil {
		return Request{}, ErrRequest
	}
	canonical, err := r.Bytes()
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return Request{}, ErrRequest
	}
	return r, nil
}

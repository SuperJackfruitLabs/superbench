// Package serviceidentity reads the fixed, nonsecret installation authority.
// A registry describes an identity; only resolution of the physical installed
// document against actual process credentials creates runtime authority.
package serviceidentity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

const Version = "superbench.service-identity.v1"
const FixedRegistry = "/etc/superbench/service-identity.json"
const MaxRegistry = payloadpolicy.MaxPolicy + 1024

var ErrAuthority = errors.New("invalid installed service identity authority")

// Registry contains no credentials, commands, selectable helper paths or
// runtime permission. It is derived from the same Policy as the private helper
// policy. Constructing or parsing it never creates an Authority.
type Registry struct {
	Version       string
	LayoutVersion string
	Policy        payloadpolicy.Policy
	PolicySHA256  string
}

// ForPolicy derives the public registry from the exact validated private helper
// policy. It creates no runtime authority and never reads or writes installation
// files.
func ForPolicy(p payloadpolicy.Policy) (Registry, error) {
	raw, err := p.Bytes()
	if err != nil {
		return Registry{}, ErrAuthority
	}
	digest := sha256.Sum256(raw)
	return Registry{Version, payloadpolicy.LayoutVersion, p, hex.EncodeToString(digest[:])}, nil
}

func (r Registry) Bytes() ([]byte, error) {
	policy, err := r.Policy.Bytes()
	if err != nil || r.Version != Version || r.LayoutVersion != payloadpolicy.LayoutVersion {
		return nil, ErrAuthority
	}
	digest := sha256.Sum256(policy)
	if r.PolicySHA256 != hex.EncodeToString(digest[:]) {
		return nil, ErrAuthority
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxRegistry {
		return nil, ErrAuthority
	}
	return raw, nil
}

// ParseRegistry accepts only the canonical representation, with optional JSON
// whitespace. Duplicate, reordered, unknown and incorrectly typed fields refuse.
func ParseRegistry(raw []byte) (Registry, error) {
	var r Registry
	if len(raw) > MaxRegistry || !utf8.Valid(raw) || json.Unmarshal(raw, &r) != nil {
		return Registry{}, ErrAuthority
	}
	canonical, err := r.Bytes()
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return Registry{}, ErrAuthority
	}
	return r, nil
}

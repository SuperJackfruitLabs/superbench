package payloadexec

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

const MaxPolicy = 64 << 10
const FixedPolicy = "/etc/superbench/payload-policy.json"

func (p Policy) Bytes() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > MaxPolicy {
		return nil, ErrPolicy
	}
	return raw, nil
}

// ParsePolicy accepts the canonical fixed-policy document with optional JSON
// whitespace. Duplicate, unknown, reordered or incorrectly typed fields refuse.
func ParsePolicy(raw []byte) (Policy, error) {
	var p Policy
	if len(raw) > MaxPolicy || !utf8.Valid(raw) || json.Unmarshal(raw, &p) != nil {
		return Policy{}, ErrPolicy
	}
	canonical, err := p.Bytes()
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return Policy{}, ErrPolicy
	}
	return p, nil
}

package freshcleanup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

func digest(raw []byte) string                  { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func (a Approval) digestBytes() ([]byte, error) { a.SHA256 = ""; return json.Marshal(a) }

func (a Approval) Bytes(p payloadpolicy.Policy, b Binding) ([]byte, error) {
	if err := a.Validate(p, b); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

// Parse requires the canonical complete model, allowing only JSON whitespace.
// Duplicate/unknown/reordered fields and tampered metadata refuse. Successful
// parsing does not authorize any cleanup, retry or change to the original intent.
func Parse(raw []byte, p payloadpolicy.Policy, b Binding) (Approval, error) {
	var a Approval
	if len(raw) > MaxApproval || !utf8.Valid(raw) || json.Unmarshal(raw, &a) != nil {
		return Approval{}, ErrApproval
	}
	canonical, err := a.Bytes(p, b)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(canonical, compact.Bytes()) {
		return Approval{}, ErrApproval
	}
	return a, nil
}

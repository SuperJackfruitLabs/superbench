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

func (a Approval) Bytes(p payloadpolicy.Policy, b Binding, owner Owner, assignment Assignment) ([]byte, error) {
	if err := a.Validate(p, b, owner, assignment); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}

// Parse requires the canonical complete model, allowing only JSON whitespace.
// Duplicate/unknown/reordered fields and tampered metadata refuse. Successful
// parsing does not authorize any cleanup, retry or change to the original intent.
func Parse(raw []byte, p payloadpolicy.Policy, b Binding, owner Owner, assignment Assignment) (Approval, error) {
	var a Approval
	if len(raw) > MaxApproval || !utf8.Valid(raw) || json.Unmarshal(raw, &a) != nil {
		return Approval{}, ErrApproval
	}
	canonical, err := a.Bytes(p, b, owner, assignment)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(canonical, compact.Bytes()) {
		return Approval{}, ErrApproval
	}
	return a, nil
}

// Rebase maps a retained approval observed in an earlier boot onto device, the
// pinned data volume's device number in this boot, and returns its canonical
// encoding. A loop or block device can be renumbered at boot, and a crash during
// cleanup is exactly when a retained approval is retried. Validation puts every
// node of an approval on its one RootDevice, so only that number moves. The
// document must already be the canonical, self-digested form; the caller must
// still Parse the result against its binding, and must bind the data volume by
// its UUID (the operation authority) before treating device as that volume.
func Rebase(raw []byte, device uint64) ([]byte, error) {
	var a Approval
	if device == 0 || len(raw) > MaxApproval || !utf8.Valid(raw) || json.Unmarshal(raw, &a) != nil {
		return nil, ErrApproval
	}
	canonical, err := json.Marshal(a)
	var compact bytes.Buffer
	unsigned, digestErr := a.digestBytes()
	if err != nil || digestErr != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(canonical, compact.Bytes()) || a.SHA256 != digest(unsigned) {
		return nil, ErrApproval
	}
	from := a.Binding.RootDevice
	move := func(d *uint64) {
		if *d == from {
			*d = device
		}
	}
	move(&a.Binding.RootDevice)
	move(&a.Observation.Parent.Device)
	move(&a.Observation.Tree.Root.Device)
	for i := range a.Observation.Tree.Entries {
		move(&a.Observation.Tree.Entries[i].Device)
	}
	for i := range a.Observation.Owners {
		move(&a.Observation.Owners[i].Device)
	}
	if unsigned, err = a.digestBytes(); err != nil {
		return nil, ErrApproval
	}
	a.SHA256 = digest(unsigned)
	return json.Marshal(a)
}

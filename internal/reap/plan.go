package reap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Plan is a local administrative approval, not a socket API message. The
// optional final field preserves v1 journal digests while v2 pins the deadline.
type Plan struct {
	Version      string
	Key          string
	Path         string
	Checkpoint   string
	Tree         Tree
	Digest       string
	ReapAfter    string `json:",omitempty"`
	FreezeDigest string `json:",omitempty"`
}

func (p Plan) DigestValue() (string, error) {
	p.Digest = ""
	raw, e := json.Marshal(p)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

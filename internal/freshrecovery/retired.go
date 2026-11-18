package freshrecovery

import (
	"crypto/sha256"
	"encoding/hex"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshgeneration"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
)

// retiredSpec requires prior, the intent root read for s, to hold s itself
// (the same canonical spec bytes, so the same workspace, the daemon sent) and
// root's retirement to be of exactly that intent (freshgeneration.Retired).
func retiredSpec(prior freshgeneration.Prior, s payloadbootstrap.Spec, authority, root string) error {
	raw, err := s.Bytes()
	sum := sha256.Sum256(raw)
	if err != nil || prior.SpecSHA256 != hex.EncodeToString(sum[:]) {
		return ErrRecovery
	}
	return freshgeneration.Retired(prior, authority, root)
}

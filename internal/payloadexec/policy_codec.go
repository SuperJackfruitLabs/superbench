package payloadexec

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"

const MaxPolicy = payloadpolicy.MaxPolicy
const FixedPolicy = payloadpolicy.FixedPolicy

// ParsePolicy preserves the canonical helper policy decoder entry point.
func ParsePolicy(raw []byte) (Policy, error) { return payloadpolicy.ParsePolicy(raw) }

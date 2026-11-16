//go:build !linux

package payloadexec

// Managed privilege separation requires the reviewed Linux helper boundary.
func LoadFixedPolicy() (Policy, error) { return Policy{}, ErrPolicy }

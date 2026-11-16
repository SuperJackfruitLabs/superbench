package serviceidentity

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"

// Authority is a snapshot of one physical installed registry bound to the
// actual service process. Its zero value denies execution. It carries no open
// descriptors and grants neither a lease nor permission to launch a command.
type Authority struct {
	registry Registry
	physical *snapshot
}

// Policy returns a value copy only after rechecking the physical registry and
// actual process identity. Callers still need their existing current ownership,
// registered layout, scope and atomic launch authorization.
func (a Authority) Policy() (payloadpolicy.Policy, error) {
	if err := a.Recheck(); err != nil {
		return payloadpolicy.Policy{}, err
	}
	return a.registry.Policy, nil
}

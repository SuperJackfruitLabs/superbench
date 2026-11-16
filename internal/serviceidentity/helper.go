package serviceidentity

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"

// HelperRegistry is a physical installed-registry snapshot for the dedicated
// root helper. It is distinct from daemon Authority, has no execution method,
// and does not establish that the helper's private policy agrees with it.
// Its zero value refuses. No descriptors escape the bounded registry read.
type HelperRegistry struct {
	registry Registry
	physical *snapshot
}

// Policy returns installed metadata only, after checking the original physical
// registry and actual root identity. The helper must independently load its
// fixed private policy and enforce its existing scope/layout/launch boundary.
func (h HelperRegistry) Policy() (payloadpolicy.Policy, error) {
	if err := h.Recheck(); err != nil {
		return payloadpolicy.Policy{}, err
	}
	return h.registry.Policy, nil
}

package hoststorage

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"

// The registered-allocation refusals live with the ledger format they guard.
var ErrFreshAllocation, ErrRegisteredAllocation = allocationledger.ErrFreshAllocation, allocationledger.ErrRegisteredAllocation

func (p Policy) refuseRegisteredAllocation(key string) error {
	return allocationledger.RefuseRegistered(p.Ledger, key)
}

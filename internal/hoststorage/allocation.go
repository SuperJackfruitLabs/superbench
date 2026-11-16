package hoststorage

import (
	"context"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storageproof"
)

type AllocationObservation = storageproof.AllocationObservation

func ParseAllocationObservation(raw []byte) (AllocationObservation, error) {
	return storageproof.ParseAllocationObservation(raw)
}

// InspectAllocation discovers only the exact logical key in an existing bound
// ledger. It never applies quota, repairs an anchor or creates a checkout.
func (w Worker) InspectAllocation(ctx context.Context, key string) (AllocationObservation, error) {
	s, err := w.inspectStorageRoot(ctx, key, nil, true, storageCommand)
	return AllocationObservation(s), err
}

func (w Worker) VerifyAllocation(ctx context.Context, approved AllocationObservation) error {
	if err := approved.Validate(); err != nil {
		return err
	}
	if approved.Root != w.Data {
		return errors.New("allocation observation belongs to another data root")
	}
	actual, err := w.InspectAllocation(ctx, approved.BenchKey)
	if err != nil {
		return err
	}
	if actual != approved {
		return errors.New("allocation authority changed since inspection")
	}
	return nil
}

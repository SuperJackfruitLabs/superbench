//go:build linux

package freshrecovery

import (
	"context"
	"errors"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshgeneration"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

// InspectAbsent is root's read-only answer to the daemon whose record of s,
// a create that never completed, holds the key: whether root holds nothing of
// it (no intent, transition or controls for its generation, no allocation of
// its own, no leaf: freshretirement.NeverAllocated), as a create refused
// before its intent (capacity_exhausted) leaves. A later generation's
// predecessor must be one Begin would follow. It writes nothing.
func InspectAbsent(ctx context.Context, s payloadbootstrap.Spec) (quotahelper.AbsentResult, error) {
	var zero quotahelper.AbsentResult
	storage, authority, err := inspection(ctx, s)
	if err != nil {
		return zero, err
	}
	var predecessor *hoststorage.Assignment
	if s.Generation != 0 {
		prior, plan, err := payloadbootstrap.ReadPrior(storage.Ledger, s.Key, freshgeneration.Before(s.Generation))
		if err == nil {
			err = freshgeneration.Follows(prior, s.Key, s.Previous, s.Generation, authority, filepath.Join(storage.Root, "benches", s.Key))
		}
		if err == nil && plan != nil {
			err = freshretirement.Retired(ctx, storage, plan.Storage)
		}
		if err != nil {
			return zero, errors.Join(ErrRecovery, err)
		}
		a := hoststorage.Assignment(prior.Allocation())
		predecessor = &a
	}
	stem := filepath.Join(storage.Ledger, "payload-bootstrap", payloadbootstrap.Stem(s))
	if err = freshretirement.NeverAllocated(ctx, storage, s.Key, predecessor, stem+".json", stem+".json.transition", stem+".controls"); err != nil {
		return zero, errors.Join(ErrRecovery, err)
	}
	return quotahelper.AbsentResult{Version: quotahelper.AbsentVersion, Key: s.Key, ID: s.ID, Generation: s.Generation}, nil
}

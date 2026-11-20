//go:build linux

package freshrecovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshgeneration"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

// InspectAbsent is root's read-only answer to the daemon, whose record of s,
// a create that never completed, still holds the key: whether root holds
// nothing at all of that create. A create refused before its bootstrap intent
// (capacity_exhausted), or one whose daemon died before the helper wrote the
// intent, leaves exactly that. Root proves it from its own files, under the
// allocation lock that a Begin holds from the intent's first write through
// its ledger save: no intent or transition file for s's generation, no
// per-create controls, no allocation of the key's own in the ledger (for a
// later generation, its row is still exactly the predecessor's retired
// tombstone, and that predecessor is one s may follow) and no leaf. It
// reserves no operation and writes nothing. The replacing create's Begin
// allocates through the ordinary checks.
func InspectAbsent(ctx context.Context, s payloadbootstrap.Spec) (quotahelper.AbsentResult, error) {
	var zero quotahelper.AbsentResult
	if s.Validate() != nil || os.Getuid() != 0 || os.Geteuid() != 0 {
		return zero, ErrRecovery
	}
	storage, err := hoststorage.Load("/etc/superbench/storage.json")
	if err != nil {
		return zero, err
	}
	authority, err := storage.OperationAuthority(ctx)
	if err != nil {
		return zero, err
	}
	var predecessor *hoststorage.Assignment
	if s.Generation != 0 {
		// The same predecessor evidence a later generation's Begin requires.
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
	err = freshretirement.NeverAllocated(ctx, storage, s.Key, predecessor, func() error {
		for _, path := range []string{stem + ".json", stem + ".json.transition", stem + ".controls"} {
			if _, err := os.Lstat(path); !errors.Is(err, syscall.ENOENT) {
				if err == nil {
					err = errors.New("the create's bootstrap evidence exists: " + filepath.Base(path))
				}
				return err
			}
		}
		return nil
	})
	if err != nil {
		return zero, errors.Join(ErrRecovery, err)
	}
	return quotahelper.AbsentResult{Version: quotahelper.AbsentVersion, Key: s.Key, ID: s.ID, Generation: s.Generation}, nil
}

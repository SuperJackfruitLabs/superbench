//go:build linux

package freshrecovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

// InspectRetired is root's read-only answer to the daemon, whose record of s,
// a create that never completed, still holds the key: whether this recovery's
// retirement retired that create's allocation. It proves it from root's own
// retained files, never from the daemon's input: the intent of s never
// completed, root's retirement reviewed exactly that intent under its denied
// operation, the ledger holds the retired project's tombstone and the leaf is
// gone. It reserves no operation and writes nothing. The next generation's
// Begin checks the same evidence again before it allocates.
func InspectRetired(ctx context.Context, s payloadbootstrap.Spec) (quotahelper.RetiredResult, error) {
	var zero quotahelper.RetiredResult
	storage, authority, err := inspection(ctx, s)
	if err != nil {
		return zero, err
	}
	// The retirement must be of exactly the intent s left (freshgeneration.Retired).
	prior, plan, err := payloadbootstrap.ReadPrior(storage.Ledger, s.Key, s.Generation)
	if err == nil && plan == nil {
		err = ErrRecovery
	}
	if err == nil {
		err = retiredSpec(prior, s, authority, filepath.Join(storage.Root, "benches", s.Key))
	}
	if err == nil {
		err = freshretirement.Retired(ctx, storage, plan.Storage)
	}
	if err != nil {
		return zero, errors.Join(ErrRecovery, err)
	}
	tombstone := plan.Storage.Assignment
	tombstone.Bytes, tombstone.Inodes = 0, 0
	return quotahelper.RetiredResult{Version: quotahelper.RetiredVersion, Key: s.Key, ID: s.ID, Generation: s.Generation, Allocation: tombstone}, nil
}

// inspection is a read-only inspection's direct-root storage and authority.
func inspection(ctx context.Context, s payloadbootstrap.Spec) (hoststorage.Policy, string, error) {
	if s.Validate() != nil || os.Getuid() != 0 || os.Geteuid() != 0 {
		return hoststorage.Policy{}, "", ErrRecovery
	}
	storage, err := hoststorage.Load("/etc/superbench/storage.json")
	if err != nil {
		return storage, "", err
	}
	authority, err := storage.OperationAuthority(ctx)
	return storage, authority, err
}

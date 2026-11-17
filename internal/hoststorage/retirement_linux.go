//go:build linux

package hoststorage

import (
	"context"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storageproof"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// RetireApproved changes only the existing ledger reservation. It NEVER
// removes a pathname; the exact fixed leaf must already be absent.
func (p Policy) RetireApproved(ctx context.Context, a RetirementApproval) (Assignment, error) {
	return p.retireApproved(ctx, a, false, false)
}

// VerifyRetiredStorage is read-only and accepts only the exact original approval
// after its project has become a tombstone. Normal inspection remains fail closed.
func (p Policy) VerifyRetiredStorage(ctx context.Context, a RetirementApproval) (Assignment, error) {
	return p.retireApproved(ctx, a, true, false)
}

// VerifyRetirementAuthority proves only the original storage/ledger binding.
// It makes NO claim about the current source leaf or its writer/absence state.
func (p Policy) VerifyRetirementAuthority(ctx context.Context, a RetirementApproval) (Assignment, error) {
	return p.retireApproved(ctx, a, false, true)
}

func (p Policy) retireApproved(ctx context.Context, a RetirementApproval, verifyOnly, authorityOnly bool) (Assignment, error) {
	var zero Assignment
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := a.validate(); err != nil {
		return zero, err
	}
	if os.Geteuid() != 0 {
		return zero, errors.New("approved quota retirement requires root")
	}
	if err := p.Validate(); err != nil {
		return zero, err
	}
	if a.Storage.Root != p.Root {
		return zero, errors.New("retirement belongs to another fixed policy root")
	}
	if err := rootOwnedDirectory(p.Ledger, true); err != nil {
		return zero, err
	}
	lock, err := openStorageFile(filepath.Join(p.Ledger, "lock"))
	if err != nil {
		return zero, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return zero, errors.New("quota ledger is busy")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// bound is the data device this verification observed; the benches parent is
	// opened on that same filesystem, and a later verification must agree.
	var bound uint64
	checkStorage := func() error {
		current, e := p.verifyStorage(ctx, a.Key, true, &a.Storage.Assignment)
		if e != nil {
			return e
		}
		expected := a.Storage
		expected.ConfigSHA256 = "" // independently bound by Worker, not root policy input
		if expected.Matches(current) != nil || bound != 0 && current.Device != bound {
			return errors.New("retirement storage authority changed")
		}
		bound = current.Device
		return nil
	}
	if err = checkStorage(); err != nil {
		return zero, err
	}
	raw, err := readStorageFile(filepath.Join(p.Ledger, "assignments.json"), 16<<20)
	if err != nil {
		return zero, err
	}
	anchor, err := readStorageFile(filepath.Join(p.Root, ".superbench-quota-owner.json"), 4096)
	if err != nil {
		return zero, err
	}
	_, _, retired, err := p.retirementRecords(a.Key, raw, anchor, a.Storage.Assignment)
	if err != nil {
		return zero, err
	}
	if verifyOnly && !retired {
		return zero, errors.New("approved source allocation has not been retired")
	}
	parent, err := openRetirementParent(a.Storage, &a.Root, bound)
	if err != nil {
		return zero, err
	}
	defer parent.Close()
	if authorityOnly {
		actual := a.Storage.Assignment
		if retired {
			actual.Bytes, actual.Inodes = 0, 0
		}
		return actual, ctx.Err()
	}
	checkRoot := func(requireAbsent bool) (bool, error) {
		// Reopen the named parent too: an open fd alone cannot authorize a replaced
		// fixed path. The managed benches parent is root-owned and not worker-writable.
		current, e := openRetirementParent(a.Storage, &a.Root, bound)
		if e != nil {
			return false, e
		}
		defer current.Close()
		return checkRetirementRoot(parent, a, requireAbsent)
	}
	// This helper has no pathname deletion authority, including on its first
	// call. The archive owner must already have removed its approved exact root.
	if _, err = checkRoot(true); err != nil {
		return zero, err
	}
	if err = p.zeroRetirementUsage(ctx, a.Storage.Assignment.ProjectID); err != nil {
		return zero, err
	}
	if err = checkStorage(); err != nil {
		return zero, err
	}
	if _, err = checkRoot(true); err != nil {
		return zero, err
	}
	if err = ctx.Err(); err != nil {
		return zero, err
	}
	tombstone := a.Storage.Assignment
	tombstone.Bytes, tombstone.Inodes = 0, 0
	if !retired {
		// The exclusive existing lock still protects exactly the validated ledger.
		var l ledger
		if err = strictStorageJSON(raw, &l); err != nil {
			return zero, err
		}
		l.Assignments["bench:"+a.Key] = tombstone
		if err = p.checkLedger(l); err != nil {
			return zero, err
		}
		if err = save(p.Ledger, filepath.Join(p.Ledger, "assignments.json"), l); err != nil {
			return zero, err
		}
	}
	return tombstone, ctx.Err()
}

func verifyRetirementUsage(raw []byte, id uint32) error { return storageproof.VerifyUnused(raw, id) }

func (p Policy) zeroRetirementUsage(ctx context.Context, id uint32) error {
	for _, unit := range []string{"-b", "-i"} {
		report, err := storageCommand(ctx, "/usr/sbin/xfs_quota", "-x", "-c", fmt.Sprintf("report -p -n %s -N -L %d -U %d", unit, id, id), p.Root)
		if err != nil {
			return err
		}
		if err = verifyRetirementUsage(report, id); err != nil {
			return err
		}
	}
	return nil
}

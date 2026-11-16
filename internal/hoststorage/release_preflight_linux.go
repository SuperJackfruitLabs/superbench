//go:build linux

package hoststorage

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storageproof"
	"os"
	"path/filepath"
	"syscall"
)

// A known ordinary allocation cannot become Fresh. Check it without Apply so
// refusal cannot create shared quota state; Release rechecks after Apply too.
func (p Policy) checkLegacyRelease(key string, project uint32) error {
	if os.Geteuid() != 0 {
		return errors.New("quota helper requires root")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if err := rootOwnedDirectory(p.Ledger, true); err != nil {
		return err
	}
	lock, err := openStorageFile(filepath.Join(p.Ledger, "lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	raw, err := readStorageFile(filepath.Join(p.Ledger, "assignments.json"), 16<<20)
	if err != nil {
		return err
	}
	fresh, err := storageproof.LegacyReleaseLedger(p.Root, p.binding(), p.upgradingInodes, key, project, raw)
	if fresh {
		return ErrRegisteredAllocation
	}
	return err
}

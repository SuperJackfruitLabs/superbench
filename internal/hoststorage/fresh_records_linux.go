//go:build linux

package hoststorage

import (
	"encoding/json"
	"errors"
	"os"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationjournal"
)

// FreshAllocationRecords encodes no assignments and performs no filesystem
// effects. The initializer's expiring capability must name this exact ledger.
func (p Policy) FreshAllocationRecords(fresh quotaoperationjournal.FreshLedger, uuid string) ([]byte, []byte, error) {
	if p.Validate() != nil || p.OperationJournal != "indexed-v2" || !volumePattern.MatchString(uuid) {
		return nil, nil, errors.New("fresh allocation records refused")
	}
	dev, ino, err := fresh.Identity()
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(p.Ledger)
	if err != nil {
		return nil, nil, err
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || st.Uid != 0 || st.Mode&07777 != 0700 || uint64(st.Dev) != dev || st.Ino != ino {
		return nil, nil, errors.New("fresh ledger capability differs from policy")
	}
	l := ledger{Root: p.Root, VolumeID: uuid, Next: 10000, Assignments: map[string]Assignment{}}
	p.bindLedger(&l)
	header, err := json.Marshal(l)
	if err != nil {
		return nil, nil, err
	}
	anchor, err := json.Marshal(ownerRecord{Root: p.Root, Ledger: p.Ledger})
	return header, anchor, err
}

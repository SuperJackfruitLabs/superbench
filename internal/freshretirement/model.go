// Package freshretirement retires only an absent Fresh allocation's reservation.
// It never deletes paths, configures quotas, or proves controller quiescence.
// A maintenance caller must separately bind retained bootstrap provenance,
// permanent operation denial and actual stopped-controller authority. The
// reclaim of a released bench is the one caller the fixed helper's sudo route
// reaches; it binds its own reserved operation and retained reclaiming intent.
package freshretirement

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storageproof"
)

const Version = "superbench.fresh-absent-retirement.v1"

var ErrRetirement = errors.New("fresh absent allocation retirement refused")
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Parent struct {
	Device, Inode  uint64
	Mode, UID, GID uint32
}

// Approval must be retained in an independently authorized, durable maintenance
// intent before RetireAbsent. Authority pins immutable host storage; Assignment
// separately pins the selected mutable Fresh reservation. This is not evidence
// of a completed bootstrap, enforced nonzero quota caps or a stopped writer.
type Approval struct {
	Version, Key, Authority string
	Assignment              hoststorage.Assignment
	Parent                  Parent
}

// Validate checks the complete immutable approval before it is durably retained.
// It does not establish current storage, usage or controller authority.
func (a Approval) Validate(p hoststorage.Policy) error { return a.validate(p) }

func (a Approval) validate(p hoststorage.Policy) error {
	if p.Validate() != nil || p.OperationJournal != "indexed-v2" || a.Version != Version || !allocationledger.Key.MatchString(a.Key) || !digest.MatchString(a.Authority) {
		return ErrRetirement
	}
	v := a.Assignment
	if v.Path != filepath.Join(p.Root, "benches", a.Key) || v.ProjectID < 10000 || v.Bytes < 1024 || v.Bytes%1024 != 0 || v.Bytes > p.BenchBytes || v.Inodes != p.BenchInodes {
		return ErrRetirement
	}
	parent := a.Parent
	if parent.Device == 0 || parent.Inode == 0 || parent.UID != 0 || os.FileMode(parent.Mode)&os.ModeType != os.ModeDir || os.FileMode(parent.Mode).Perm()&0022 != 0 {
		return ErrRetirement
	}
	return nil
}

// selected validates every row and the exact selected Fresh marker. A retained
// original approval is required to recognize a tombstone; never reconstruct its
// old quota from the policy ceiling or another allocation's metadata.
func selected(p hoststorage.Policy, key string, raw []byte, original *hoststorage.Assignment) (allocationledger.Ledger, hoststorage.Assignment, bool, error) {
	var l allocationledger.Ledger
	if !allocationledger.Key.MatchString(key) || storageproof.StrictJSON(raw, &l) != nil || p.Validate() != nil || p.OperationJournal != "indexed-v2" || l.FormatVersion < 3 || !l.Bound() || l.Validate(p.Root, p.AllocationBinding(), false) != nil {
		return l, hoststorage.Assignment{}, false, ErrRetirement
	}
	canonical, err := json.MarshalIndent(l, "", "  ")
	if err != nil || !bytes.Equal(raw, canonical) {
		return l, hoststorage.Assignment{}, false, ErrRetirement
	}
	a, ok := l.Assignments["bench:"+key]
	if !ok || l.Fresh["bench:"+key] != a.ProjectID {
		return l, hoststorage.Assignment{}, false, ErrRetirement
	}
	retired := a.Bytes == 0 && a.Inodes == 0
	if original == nil {
		if retired {
			return l, hoststorage.Assignment{}, false, ErrRetirement
		}
	} else if a.ProjectID != original.ProjectID || a.Path != original.Path || !retired && a != *original {
		return l, hoststorage.Assignment{}, false, ErrRetirement
	} else {
		a = *original
	}
	return l, a, retired, nil
}

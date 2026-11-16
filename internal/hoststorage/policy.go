// Package hoststorage implements the narrowly scoped host quota policy.
package hoststorage

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
	"path/filepath"
	"regexp"
)

type Policy struct {
	// OperationJournal is selected only by installed policy. Empty retains the
	// legacy helper; indexed-v2 requires retained indexed history on every call.
	OperationJournal string `json:",omitempty"`
	Root             string
	Ledger           string
	OwnerUID         int
	OwnerGID         int
	BenchBytes       uint64
	StoreBytes       uint64
	MirrorBytes      uint64
	ReserveBytes     uint64
	BenchInodes      uint64
	StoreInodes      uint64
	MirrorInodes     uint64
	ReserveInodes    uint64
	upgradingInodes  bool
}
type Assignment = quotahelper.Assignment
type CapacityError = quotahelper.CapacityError

type ledger = allocationledger.Ledger

var Key = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var volumePattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var safePath = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)

func (p Policy) Validate() error {
	if p.OperationJournal != "" && p.OperationJournal != "indexed-v2" {
		return errors.New("unsupported operation journal policy")
	}
	if !safePath.MatchString(p.Root) || filepath.Clean(p.Root) != p.Root || p.Root == "/" {
		return errors.New("quota root must be an absolute normalized path without shell/parser metacharacters")
	}
	if !filepath.IsAbs(p.Ledger) || filepath.Clean(p.Ledger) != p.Ledger || p.Ledger == "/" {
		return errors.New("invalid ledger path")
	}
	for _, pair := range [][2]string{{p.Root, p.Ledger}, {p.Ledger, p.Root}} {
		rel, e := filepath.Rel(pair[0], pair[1])
		if e != nil {
			return e
		}
		if rel == "." || (rel != ".." && len(rel) > 0 && rel[:min(3, len(rel))] != "../") {
			return errors.New("root and privileged ledger must be separate")
		}
	}
	if p.OwnerUID < 0 || p.OwnerGID < 0 {
		return errors.New("invalid owner")
	}
	for _, size := range []uint64{p.BenchBytes, p.StoreBytes, p.MirrorBytes, p.ReserveBytes} {
		if size < 1024 || size%1024 != 0 || size > 1<<50 {
			return errors.New("budgets must be positive whole KiB, at most 1 PiB")
		}
	}
	for _, count := range []uint64{p.BenchInodes, p.StoreInodes, p.MirrorInodes, p.ReserveInodes} {
		if count == 0 || count > 1<<40 {
			return errors.New("inode budgets must be explicit positive counts, at most 2^40")
		}
	}
	return nil
}
func Load(path string) (Policy, error) {
	var p Policy
	if err := storagecustody.LoadPolicy(path, &p); err != nil {
		return p, err
	}
	return p, p.Validate()
}

func rootOwnedDirectory(path string, private bool) error {
	return storagecustody.RootOwnedDirectory(path, private)
}
func save(dir, path string, v any) error { return allocationledger.Save(dir, path, v) }

// Root-owned children beneath a root-owned sticky directory are protected too;
// other writable ancestors would let a caller swap a checked policy/ledger path.
func trustedAncestors(path string) error { return storagecustody.TrustedAncestors(path) }
func (p Policy) checkLedger(l ledger) error {
	return l.Validate(p.Root, p.binding(), p.upgradingInodes)
}

type ownerRecord = storagecustody.OwnerRecord

func (p Policy) anchor() error { return storagecustody.Anchor(p.Root, p.Ledger) }

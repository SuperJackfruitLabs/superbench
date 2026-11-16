// Package hoststorage implements the narrowly scoped host quota policy.
package hoststorage

import (
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
	"os"
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

type ledger struct {
	FormatVersion  int            `json:",omitempty"`
	FilesystemUUID string         `json:",omitempty"`
	VolumeID       string         `json:",omitempty"`
	Binding        *policyBinding `json:",omitempty"`
	Root           string
	Next           uint32
	Assignments    map[string]Assignment
}

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
func save(dir, path string, v any) error {
	raw, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, "ledger-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(raw); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// Root-owned children beneath a root-owned sticky directory are protected too;
// other writable ancestors would let a caller swap a checked policy/ledger path.
func trustedAncestors(path string) error { return storagecustody.TrustedAncestors(path) }
func (p Policy) checkLedger(l ledger) error {
	if e := p.ledgerHeader(l); e != nil {
		return e
	}
	if l.Root != p.Root || l.Assignments == nil || l.Next < 10000 {
		return errors.New("privileged ledger conflicts with policy")
	}
	seen := map[uint32]bool{}
	for name, a := range l.Assignments {
		var path string
		var bytes, inodes uint64
		switch name {
		case "store":
			path = filepath.Join(p.Root, "store")
			bytes = p.StoreBytes
			inodes = p.StoreInodes
			if a.ProjectID != 100 {
				return errors.New("invalid store project")
			}
		case "mirrors":
			path = filepath.Join(p.Root, "mirrors")
			bytes = p.MirrorBytes
			inodes = p.MirrorInodes
			if a.ProjectID != 101 {
				return errors.New("invalid mirror project")
			}
		default:
			if len(name) < 7 || name[:6] != "bench:" || !Key.MatchString(name[6:]) || a.ProjectID < 10000 || a.ProjectID >= l.Next {
				return errors.New("invalid bench allocation")
			}
			path = filepath.Join(p.Root, "benches", name[6:])
			bytes = p.BenchBytes
			if l.FormatVersion == 2 && a.Bytes != 0 {
				if a.Bytes < 1024 || a.Bytes%1024 != 0 || a.Bytes > p.BenchBytes {
					return errors.New("bench allocation exceeds pinned quota ceiling")
				}
				bytes = a.Bytes
			}
			inodes = p.BenchInodes
			if a.Bytes == 0 {
				bytes = 0
				inodes = 0
			} // A zero-byte bench assignment is a permanent retired ID.
		}
		if seen[a.ProjectID] || a.Path != path || a.Bytes != bytes || (!p.upgradingInodes || a.Bytes == 0) && a.Inodes != inodes {
			return errors.New("ledger allocation conflicts with policy; explicit migration required")
		}
		seen[a.ProjectID] = true
	}
	return nil
}

type ownerRecord = storagecustody.OwnerRecord

func (p Policy) anchor() error { return storagecustody.Anchor(p.Root, p.Ledger) }

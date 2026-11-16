// Package allocationledger owns the durable quota allocation format and replay guards.
package allocationledger

import (
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var Key = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var volumePattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Ledger struct {
	FormatVersion  int      `json:",omitempty"`
	FilesystemUUID string   `json:",omitempty"`
	VolumeID       string   `json:",omitempty"`
	Binding        *Binding `json:",omitempty"`
	Root           string
	Next           uint32
	Fresh          map[string]uint32 `json:",omitempty"`
	Assignments    map[string]quotapolicy.Assignment
}

type Binding struct {
	OperationJournal                                      string `json:",omitempty"`
	OwnerUID, OwnerGID                                    int
	BenchBytes, StoreBytes, MirrorBytes, ReserveBytes     uint64
	BenchInodes, StoreInodes, MirrorInodes, ReserveInodes uint64
}

func (l Ledger) FreshBench(key string) bool {
	_, exists := l.Fresh["bench:"+key]
	return exists
}

func (l Ledger) Filesystem() string {
	if l.FormatVersion >= 2 {
		return l.FilesystemUUID
	}
	return l.VolumeID
}

func (l Ledger) Header(expected Binding, upgrading bool) error {
	if l.FormatVersion != 3 && len(l.Fresh) != 0 {
		return errors.New("fresh markers require quota ledger format 3")
	}
	switch l.FormatVersion {
	case 0:
		if expected.OperationJournal != "" {
			return errors.New("indexed operation policy requires a bound v2 ledger")
		}
		if l.VolumeID == "" || l.FilesystemUUID != "" || l.Binding != nil {
			return errors.New("invalid legacy ledger identity")
		}
	case 2, 3:
		if l.VolumeID != "" || !volumePattern.MatchString(l.FilesystemUUID) || l.Binding == nil {
			return errors.New("invalid quota ledger identity or policy binding")
		}
		actual, expected := *l.Binding, expected
		if upgrading {
			actual.BenchInodes, actual.StoreInodes, actual.MirrorInodes, actual.ReserveInodes = 0, 0, 0, 0
			expected.BenchInodes, expected.StoreInodes, expected.MirrorInodes, expected.ReserveInodes = 0, 0, 0, 0
		}
		if actual != expected {
			return errors.New("quota policy differs from the pinned ledger; explicit migration required")
		}
	default:
		return errors.New("unsupported quota ledger format")
	}
	return nil
}

func (l *Ledger) Bind(binding Binding) {
	identity := l.Filesystem()
	if l.FormatVersion < 2 {
		l.FormatVersion = 2
	}
	l.FilesystemUUID, l.VolumeID = identity, ""
	l.Binding = &binding
}

func (l Ledger) Validate(root string, p Binding, upgrading bool) error {
	if e := l.Header(p, upgrading); e != nil {
		return e
	}
	if l.Root != root || l.Assignments == nil || l.Next < 10000 {
		return errors.New("privileged ledger conflicts with policy")
	}
	seen := map[uint32]bool{}
	for name, a := range l.Assignments {
		var path string
		var bytes, inodes uint64
		switch name {
		case "store":
			path = filepath.Join(root, "store")
			bytes = p.StoreBytes
			inodes = p.StoreInodes
			if a.ProjectID != 100 {
				return errors.New("invalid store project")
			}
		case "mirrors":
			path = filepath.Join(root, "mirrors")
			bytes = p.MirrorBytes
			inodes = p.MirrorInodes
			if a.ProjectID != 101 {
				return errors.New("invalid mirror project")
			}
		default:
			if len(name) < 7 || name[:6] != "bench:" || !Key.MatchString(name[6:]) || a.ProjectID < 10000 || a.ProjectID >= l.Next {
				return errors.New("invalid bench allocation")
			}
			path = filepath.Join(root, "benches", name[6:])
			bytes = p.BenchBytes
			if l.FormatVersion >= 2 && a.Bytes != 0 {
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
		if seen[a.ProjectID] || a.Path != path || a.Bytes != bytes || (!upgrading || a.Bytes == 0) && a.Inodes != inodes {
			return errors.New("ledger allocation conflicts with policy; explicit migration required")
		}
		seen[a.ProjectID] = true
	}
	for name, id := range l.Fresh {
		a, ok := l.Assignments[name]
		if l.FormatVersion != 3 || !strings.HasPrefix(name, "bench:") || !ok || a.ProjectID != id {
			return errors.New("invalid fresh allocation marker")
		}
	}
	return nil
}

func Save(dir, path string, v any) error {
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

func (p Binding) Request(kind string, size uint64) error {
	if size != 0 && (kind != "bench" || size < 1024 || size%1024 != 0 || size > p.BenchBytes) {
		return errors.New("requested bench quota must be whole KiB within the root policy ceiling")
	}
	return nil
}

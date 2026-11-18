// Package projectscan is root's read-only check that every allocated bench's
// tree is still inside its XFS quota project. A file's owner can leave a
// project without any capability; the payload filter refuses that, but trees
// written before it, or by another route, are found only by this walk.
package projectscan

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaleaf"
)

// ErrOutside reports a scan that found at least one bench outside its project.
var ErrOutside = errors.New("bench trees outside their quota projects; restore them and scan again")

// ErrUnverified reports a scan that could not finish some bench's walk, for
// example because the tree changed under it; nothing is known to be outside.
var ErrUnverified = errors.New("some bench trees could not be verified; stop their runs and scan again")

// Result is one bench: Status is "contained", "absent", "outside" (a real
// project, inherit-flag or inode-cap escape) or "unverified" (the walk could
// not finish), with Reason set for the last two.
type Result struct {
	Key       string `json:"key"`
	ProjectID uint32 `json:"project_id"`
	Fresh     bool   `json:"fresh"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

// bench is one allocated bench the ledger names below root.
type bench struct {
	key, path      string
	project        uint32
	inodes         uint64
	fresh, retired bool
}

// benches lists the ledger's allocated benches in key order. A retired
// tombstone (zero bytes and inodes) has no tree to scan.
func benches(root string, raw []byte) ([]bench, error) {
	var l allocationledger.Ledger
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, err
	}
	var out []bench
	for name, a := range l.Assignments {
		key, ok := strings.CutPrefix(name, "bench:")
		if !ok {
			continue
		}
		if !allocationledger.Key.MatchString(key) || a.Path != filepath.Join(root, "benches", key) || a.ProjectID == 0 {
			return nil, errors.New("ledger names a bench outside the managed root")
		}
		out = append(out, bench{key: key, path: a.Path, project: a.ProjectID, inodes: a.Inodes, fresh: l.FreshBench(key), retired: a.Bytes == 0 && a.Inodes == 0})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out, nil
}

// classify names a walk's error: only the walk's own escape errors are
// "outside"; anything else, such as a name that vanished or became
// unreadable during the walk, or the name bound, is "unverified".
func classify(err error) string {
	switch {
	case err == nil:
		return "contained"
	case errors.Is(err, quotaleaf.ErrLeafProject), errors.Is(err, quotaleaf.ErrTreeProject), errors.Is(err, quotaleaf.ErrTreeLimit):
		return "outside"
	}
	return "unverified"
}

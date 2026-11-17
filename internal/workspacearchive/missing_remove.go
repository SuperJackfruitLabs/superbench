package workspacearchive

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

type rootRemoval struct{ Version, Key, Path, BenchID, PlanDigest string }

func (m Manager) rootRemovalPath(key string) string {
	return filepath.Join(m.State, "aggregate-root-removal-"+key+".json")
}

// A started removal is never retried against a present root. Even an identical
// empty inode may have been reused after a prior successful rmdir. Ambiguity
// preserves the directory for an independently reviewed administrative repair.
func (m Manager) rootRemovalStarted(b *model.Bench) (bool, error) {
	raw, e := readMissingFile(m.rootRemovalPath(b.Key))
	if os.IsNotExist(e) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var p rootRemoval
	if e = json.Unmarshal(raw, &p); e != nil {
		return false, e
	}
	expected := rootRemoval{Version: "superbench.missing-root-removal.v1", Key: b.Key, Path: b.Path, BenchID: b.Workspace.ID, PlanDigest: b.Workspace.Archive.ReapingDigest}
	want, e := json.Marshal(expected)
	if e != nil {
		return false, e
	}
	if string(raw) != string(want) {
		return false, errors.New("missing root removal journal changed")
	}
	if _, e = os.Lstat(b.Path); !os.IsNotExist(e) {
		return false, errors.New("root returned after removal started; preserve for explicit fresh review")
	}
	return true, nil
}
func (m Manager) removeMissingRoot(ctx context.Context, b *model.Bench, p Plan, managed bool) error {
	started, e := m.rootRemovalStarted(b)
	if e != nil || started {
		return e
	}
	if e = checkMissingRoot(*p.Missing, managed, true); e != nil {
		return e
	}
	if !p.Missing.Root.Absent {
		if e = emptyApprovedRoot(b.Path, p.Tree); e != nil {
			return e
		}
	}
	raw, e := json.Marshal(rootRemoval{Version: "superbench.missing-root-removal.v1", Key: b.Key, Path: b.Path, BenchID: b.Workspace.ID, PlanDigest: p.Digest})
	if e != nil {
		return e
	}
	if e = m.SaveIntent(m.rootRemovalPath(b.Key), raw); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = checkMissingRoot(*p.Missing, managed, true); e != nil {
		return e
	}
	if !p.Missing.Root.Absent {
		if e = emptyApprovedRoot(b.Path, p.Tree); e != nil {
			return e
		}
		if e = os.Remove(b.Path); e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	parent, e := os.Open(filepath.Dir(b.Path))
	if e != nil {
		return e
	}
	defer parent.Close()
	if e = parent.Sync(); e != nil {
		return e
	}
	_, e = m.rootRemovalStarted(b)
	return e
}

package localprep

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Local logs are deliberately separate from supervised history and never
// eligible for automatic history collection. Exhaustion refuses new work;
// acknowledgement and ordinary stopped-owner reconciliation remain available.
func (m Manager) admit() error {
	entries, e := os.ReadDir(m.State)
	if e != nil {
		return e
	}
	var total int64
	count := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "local-preparation-") || (!strings.HasSuffix(entry.Name(), ".log") && !strings.HasSuffix(entry.Name(), ".record")) {
			continue
		}
		info, e := os.Lstat(filepath.Join(m.State, entry.Name()))
		if e != nil || !info.Mode().IsRegular() {
			return errors.New("local preparation log identity changed")
		}
		count++
		total += info.Size()
	}
	limit := m.MaxLogBytes
	if limit == 0 {
		limit = 1 << 20
	}
	if count >= 1000 || total > 64<<20 || uint64(total)+limit > 64<<20 {
		return errors.New("local preparation logs exhausted; preserve and review retained evidence before new work")
	}
	for _, entry := range []struct {
		path    string
		minimum uint64
	}{{m.State, limit + 1<<20}, {m.Data, 64 << 20}} {
		var info syscall.Statfs_t
		if e = syscall.Statfs(entry.path, &info); e != nil {
			return e
		}
		if uint64(info.Bavail)*uint64(info.Bsize) < entry.minimum {
			return errors.New("insufficient free space for cooperative preparation")
		}
	}
	return nil
}

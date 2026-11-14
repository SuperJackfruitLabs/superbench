package output

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Reservation struct {
	Log   string
	Limit uint64
}

func Reserve(root string, per, total uint64, maxFiles int, reservations []Reservation) (uint64, error) {
	entries, e := os.ReadDir(root)
	if e != nil {
		return 0, e
	}
	sizes := map[string]uint64{}
	used := uint64(0)
	count := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "run-") || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, e := entry.Info()
		if e != nil {
			return 0, e
		}
		if !info.Mode().IsRegular() {
			return 0, errors.New("run log is not a regular file")
		}
		size := uint64(info.Size())
		if size > total || used > total-size {
			return 0, errors.New("run log budget exhausted")
		}
		used += size
		count++
		sizes[filepath.Join(root, entry.Name())] = size
	}
	outstanding := uint64(0)
	for _, execution := range reservations {
		limit := execution.Limit
		if limit == 0 {
			limit = per
		}
		size := sizes[execution.Log]
		if size < limit {
			remaining := limit - size
			if remaining > total || outstanding > total-remaining {
				return 0, errors.New("run log reservations exhausted")
			}
			outstanding += remaining
		}
	}
	if count >= maxFiles || per > total || used > total-per || outstanding > total-per-used {
		return 0, errors.New("run log budget exhausted; review retention before starting more commands")
	}
	return outstanding, nil
}

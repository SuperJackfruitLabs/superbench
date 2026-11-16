package quotapolicy

import (
	"errors"
	"strings"
)

type Assignment struct {
	Path          string
	ProjectID     uint32
	Bytes, Inodes uint64
}

// ReservationBytes counts fixed shared/reserve budgets plus every retained
// logical bench cap, then the requested new bench. It grants no allocation.
// Callers validate the authoritative ledger and individual caps separately.
func ReservationBytes(shared uint64, assignments map[string]Assignment, addBench bool, size uint64) (uint64, error) {
	used := shared
	for key, assignment := range assignments {
		bytes := assignment.Bytes
		if strings.HasPrefix(key, "bench:") {
			if used > 1<<60-bytes {
				return 0, errors.New("budget overflow")
			}
			used += bytes
		}
	}
	if addBench {
		used += size
	}
	return used, nil
}

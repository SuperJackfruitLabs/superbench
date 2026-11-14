//go:build linux

package hoststorage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func (p Policy) readQuota(id uint32, unit string) (used, hard uint64, found bool, err error) {
	report, e := xfs("/usr/sbin/xfs_quota", "-x", "-c", fmt.Sprintf("report -p -n %s -N -L %d -U %d", unit, id, id), p.Root)
	if e != nil {
		return 0, 0, false, e
	}
	for _, line := range strings.Split(report, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "#"+strconv.FormatUint(uint64(id), 10) {
			continue
		}
		used, e = strconv.ParseUint(fields[1], 10, 64)
		if e != nil {
			return 0, 0, false, e
		}
		hard, e = strconv.ParseUint(fields[3], 10, 64)
		return used, hard, true, e
	}
	return 0, 0, false, nil
}

// MigrateInodes is explicit direct-root administration after stopping workers.
// Candidate counts come only from the root policy. Allocation IDs/bytes/paths
// remain pinned. Each target is journaled before kernel effects and verified on
// every retry, including targets a previous interrupted attempt already updated.
func (p Policy) MigrateInodes() ([]Assignment, error) {
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" {
		return nil, errors.New("inode migration requires a direct root caller")
	}
	p.upgradingInodes = true
	var out []Assignment
	for _, key := range []string{"store", "mirrors"} {
		a, e := p.Apply("shared", key)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	raw, e := os.ReadFile(filepath.Join(p.Ledger, "assignments.json"))
	if e != nil {
		return nil, e
	}
	var l ledger
	if e = json.Unmarshal(raw, &l); e != nil {
		return nil, e
	}
	if e = p.checkLedger(l); e != nil {
		return nil, e
	}
	var keys []string
	for key, a := range l.Assignments {
		if strings.HasPrefix(key, "bench:") && a.Bytes != 0 {
			keys = append(keys, strings.TrimPrefix(key, "bench:"))
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		a, e := p.Apply("bench", key)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	// Normal admission must accept the final ledger; partial migration is not a
	// success claim. Each Apply holds the same privileged allocation lock.
	raw, e = os.ReadFile(filepath.Join(p.Ledger, "assignments.json"))
	if e != nil {
		return nil, e
	}
	l = ledger{}
	if e = json.Unmarshal(raw, &l); e != nil {
		return nil, e
	}
	p.upgradingInodes = false
	if e = p.checkLedger(l); e != nil {
		return nil, e
	}
	return out, nil
}

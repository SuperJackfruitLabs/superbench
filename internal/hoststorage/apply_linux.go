//go:build linux

package hoststorage

import (
	"encoding/json"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hostlayout"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func (p Policy) Apply(kind, key string) (Assignment, error) {
	return p.ApplyRequested(kind, key, 0)
}
func (p Policy) ApplyRequested(kind, key string, requested uint64) (Assignment, error) {
	return p.applyRequested(kind, key, requested, nil)
}

// ApplyFreshRequested reserves only a previously unknown bench allocation. The
// trusted helper callback runs under the allocation lock after quota confirmation
// and before ownership is released. It must journal its exact fresh root before
// exposing it. A failed callback retains the allocation; no retry adopts it.
// Callers must hold their real operation journal barrier before entering here.
func (p Policy) ApplyFreshRequested(key string, requested uint64, initialize func(Assignment) error) (Assignment, error) {
	if initialize == nil {
		return Assignment{}, ErrFreshAllocation
	}
	return p.applyRequested("bench", key, requested, initialize)
}

func (p Policy) applyRequested(kind, key string, requested uint64, initialize func(Assignment) error) (Assignment, error) {
	var zero Assignment
	if os.Geteuid() != 0 {
		return zero, errors.New("quota helper requires root")
	}
	if e := p.Validate(); e != nil {
		return zero, e
	}
	if e := p.request(kind, requested); e != nil {
		return zero, e
	}
	var name, path string
	var size, inodes uint64
	var fixed uint32
	switch kind {
	case "bench":
		if !Key.MatchString(key) {
			return zero, errors.New("invalid bench key")
		}
		name = "bench:" + key
		path = filepath.Join(p.Root, "benches", key)
		size = p.BenchBytes
		if requested != 0 {
			size = requested
		}
		inodes = p.BenchInodes
	case "shared":
		switch key {
		case "store":
			name = "store"
			path = filepath.Join(p.Root, "store")
			size = p.StoreBytes
			inodes = p.StoreInodes
			fixed = 100
		case "mirrors":
			name = "mirrors"
			path = filepath.Join(p.Root, "mirrors")
			size = p.MirrorBytes
			inodes = p.MirrorInodes
			fixed = 101
		default:
			return zero, errors.New("unknown shared resource")
		}
	default:
		return zero, errors.New("expected bench or shared")
	}
	if e := rootOwnedDirectory(p.Root, false); e != nil {
		return zero, e
	}
	var fs syscall.Statfs_t
	if e := syscall.Statfs(p.Root, &fs); e != nil {
		return zero, e
	}
	if fs.Type != 0x58465342 {
		return zero, errors.New("managed quotas require XFS")
	}
	rootInfo, e := os.Stat(p.Root)
	if e != nil {
		return zero, e
	}
	parentInfo, e := os.Stat(filepath.Dir(p.Root))
	if e != nil {
		return zero, e
	}
	if rootInfo.Sys().(*syscall.Stat_t).Dev == parentInfo.Sys().(*syscall.Stat_t).Dev {
		return zero, errors.New("quota root must be a dedicated filesystem mount")
	}

	state, e := xfs("/usr/sbin/xfs_quota", "-x", "-c", "state -p", p.Root)
	if e != nil {
		return zero, e
	}
	if !strings.Contains(state, "Accounting: ON") || !strings.Contains(state, "Enforcement: ON") {
		return zero, errors.New("project quota accounting and enforcement must be enabled")
	}
	mountIdentity, e := xfs("/usr/bin/findmnt", "--noheadings", "--output", "UUID,FSROOT", "--target", p.Root)
	if e != nil {
		return zero, e
	}
	fields := strings.Fields(mountIdentity)
	if len(fields) != 2 || fields[1] != "/" || !volumePattern.MatchString(fields[0]) {
		return zero, errors.New("dedicated filesystem root with a readable UUID is required")
	}
	volumeID := strings.ToLower(fields[0])
	// These parent directories cannot be renamed by the worker. Existing children
	// are never reassigned to another project or traversed recursively on retry.
	if e := hostlayout.PrepareBenches(p.Root); e != nil {
		return zero, e
	}
	if e := rootOwnedDirectory(filepath.Join(p.Root, "benches"), false); e != nil {
		return zero, e
	}
	if e := trustedAncestors(p.Ledger); e != nil {
		return zero, e
	}
	if e := os.MkdirAll(p.Ledger, 0700); e != nil {
		return zero, e
	}
	if e := rootOwnedDirectory(p.Ledger, true); e != nil {
		return zero, e
	}
	lock, e := os.OpenFile(filepath.Join(p.Ledger, "lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return zero, e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); e != nil {
		return zero, e
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	l := ledger{VolumeID: volumeID, Root: p.Root, Next: 10000, Assignments: map[string]Assignment{}}
	raw, e := os.ReadFile(filepath.Join(p.Ledger, "assignments.json"))
	if e == nil {
		l = ledger{}
		if e = json.Unmarshal(raw, &l); e != nil {
			return zero, e
		}
	} else if !os.IsNotExist(e) {
		return zero, e
	}
	if e = p.checkLedger(l); e != nil {
		return zero, e
	}
	if l.filesystem() != volumeID {
		return zero, errors.New("filesystem UUID differs from the allocation ledger; explicit recovery required")
	}
	if e = p.anchor(); e != nil {
		return zero, e
	}
	if l.FormatVersion != 2 || p.upgradingInodes {
		p.bindLedger(&l)
		if e = save(p.Ledger, filepath.Join(p.Ledger, "assignments.json"), l); e != nil {
			return zero, e
		}
	}

	a, known := l.Assignments[name]
	if kind == "bench" {
		if err := p.refuseRegisteredAllocation(key); err != nil {
			return zero, err
		}
	}
	if initialize != nil && known {
		return zero, ErrFreshAllocation
	}
	reserved := p.StoreInodes + p.MirrorInodes + p.ReserveInodes
	for key, assignment := range l.Assignments {
		if strings.HasPrefix(key, "bench:") && assignment.Bytes != 0 {
			if reserved > 1<<60-p.BenchInodes {
				return zero, errors.New("inode budget overflow")
			}
			reserved += p.BenchInodes
		}
	}
	if kind == "bench" && !known {
		reserved += inodes
	}
	if (!known || p.upgradingInodes) && reserved > fs.Files {
		needed, err := quotapolicy.ReservationBytes(p.StoreBytes+p.MirrorBytes+p.ReserveBytes, l.Assignments, kind == "bench" && !known, size)
		if err != nil {
			return zero, err
		}
		return zero, &CapacityError{NeededBytes: needed, AvailableBytes: fs.Blocks * uint64(fs.Bsize), NeededInodes: reserved, AvailableInodes: fs.Files}
	}

	if known {
		if a.Bytes == 0 {
			return zero, errors.New("bench project is retired; use another key")
		}
		if kind == "bench" && requested == 0 {
			size = a.Bytes
		}
		if a.Path != path || a.Bytes != size || !p.upgradingInodes && a.Inodes != inodes {
			return zero, errors.New("existing allocation differs from policy; explicit migration required")
		}
		if p.upgradingInodes {
			used, _, _, e := p.readQuota(a.ProjectID, "-i")
			if e != nil {
				return zero, e
			}
			if used > inodes {
				return zero, errors.New("inode migration would set a limit below current usage")
			}
			a.Inodes = inodes
			l.Assignments[name] = a
			if e = save(p.Ledger, filepath.Join(p.Ledger, "assignments.json"), l); e != nil {
				return zero, e
			}
		}
	} else {
		// Reserve all shared caps even before those projects are first materialized.
		used, err := quotapolicy.ReservationBytes(p.StoreBytes+p.MirrorBytes+p.ReserveBytes, l.Assignments, kind == "bench", size)
		if err != nil {
			return zero, err
		}
		if used > fs.Blocks*uint64(fs.Bsize) {
			return zero, &CapacityError{NeededBytes: used, AvailableBytes: fs.Blocks * uint64(fs.Bsize), NeededInodes: reserved, AvailableInodes: fs.Files}
		}
		if _, e = os.Lstat(path); e == nil {
			return zero, errors.New("unmanaged target exists; refusing quota adoption")
		} else if !os.IsNotExist(e) {
			return zero, e
		}
		id := fixed
		if id == 0 {
			if l.Next >= 2147483000 {
				return zero, errors.New("project IDs exhausted")
			}
			id = l.Next
			l.Next++
		}
		a = Assignment{Path: path, ProjectID: id, Bytes: size, Inodes: inodes}
		l.Assignments[name] = a
		// Persist allocation before filesystem effects. A retry uses the same ID.
		if e = save(p.Ledger, filepath.Join(p.Ledger, "assignments.json"), l); e != nil {
			return zero, e
		}
	}
	if e = os.Mkdir(path, 0700); e != nil && !os.IsExist(e) {
		return zero, e
	}
	stat, e := os.Lstat(path)
	if e != nil {
		return zero, e
	}
	if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		return zero, errors.New("quota target must be a directory, not a symlink")
	}
	info, e := xfs("/usr/sbin/xfs_io", "-c", "stat", path)
	if e != nil {
		return zero, e
	}
	project := ""
	inherit := false
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "fsxattr.projid = ") {
			project = strings.TrimPrefix(line, "fsxattr.projid = ")
		}
		if strings.HasPrefix(line, "fsxattr.xflags = ") && strings.Contains(line, "P") {
			inherit = true
		}
	}
	expected := strconv.FormatUint(uint64(a.ProjectID), 10)
	if project != expected || !inherit {
		entries, e := os.ReadDir(path)
		if e != nil {
			return zero, e
		}
		if len(entries) != 0 {
			return zero, errors.New("target contains files without matching project inheritance; refusing reassignment")
		}
		if project != "0" && project != expected {
			return zero, errors.New("target belongs to another project")
		}
		if _, e = xfs("/usr/sbin/xfs_quota", "-x", "-c", fmt.Sprintf("project -s -p %s %d", path, a.ProjectID), p.Root); e != nil {
			return zero, e
		}
	}
	if _, e = xfs("/usr/sbin/xfs_quota", "-x", "-c", fmt.Sprintf("limit -p bhard=%dk ihard=%d %d", size/1024, inodes, a.ProjectID), p.Root); e != nil {
		return zero, e
	}
	for unit, expectedLimit := range map[string]uint64{"-b": size / 1024, "-i": inodes} {
		_, hard, found, e := p.readQuota(a.ProjectID, unit)
		if e != nil {
			return zero, e
		}
		if !found || hard != expectedLimit {
			return zero, errors.New("unable to confirm project byte/inode hard limit")
		}
	}
	if initialize != nil {
		if e = initialize(a); e != nil {
			return zero, e
		}
		return a, nil
	}
	if e = os.Chown(path, p.OwnerUID, p.OwnerGID); e != nil {
		return zero, e
	}
	return a, nil
}
func xfs(binary string, args ...string) (string, error) {
	cmd := exec.Command(binary, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	out, e := cmd.CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("%s: %w: %s", filepath.Base(binary), e, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

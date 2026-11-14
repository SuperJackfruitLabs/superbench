//go:build linux

package hoststorage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Release removes only an empty logical bench directory, never file contents.
// Its zero-byte tombstone keeps the project ID/key retired and makes older
// helpers refuse this ledger rather than silently allocating it again.
func (p Policy) Release(key string, project uint32) (Assignment, error) {
	var zero Assignment
	if !Key.MatchString(key) || project < 10000 {
		return zero, errors.New("invalid bench assignment")
	}
	// Reuse enforcement, filesystem/UUID/anchor and trusted-directory validation.
	if _, e := p.Apply("shared", "store"); e != nil {
		return zero, e
	}
	lock, e := os.OpenFile(filepath.Join(p.Ledger, "lock"), os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return zero, e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); e != nil {
		return zero, e
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	raw, e := os.ReadFile(filepath.Join(p.Ledger, "assignments.json"))
	if e != nil {
		return zero, e
	}
	var l ledger
	if e = json.Unmarshal(raw, &l); e != nil {
		return zero, e
	}
	if e = p.checkLedger(l); e != nil {
		return zero, e
	}
	identity, e := xfs("/usr/bin/findmnt", "--noheadings", "--output", "UUID,FSROOT", "--target", p.Root)
	if e != nil {
		return zero, e
	}
	fields := strings.Fields(identity)
	if len(fields) != 2 || fields[1] != "/" || strings.ToLower(fields[0]) != l.filesystem() {
		return zero, errors.New("filesystem changed during release")
	}
	a, ok := l.Assignments["bench:"+key]
	if !ok || a.ProjectID != project {
		return zero, errors.New("bench allocation does not match requested project")
	}
	if a.Bytes == 0 {
		return a, nil
	}
	stat, e := os.Lstat(a.Path)
	if e == nil {
		if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
			return zero, errors.New("reap target must be an empty directory")
		}
		entries, e := os.ReadDir(a.Path)
		if e != nil {
			return zero, e
		}
		if len(entries) != 0 {
			return zero, errors.New("bench still has files; quota remains reserved")
		}
		info, e := xfs("/usr/sbin/xfs_io", "-c", "stat", a.Path)
		if e != nil {
			return zero, e
		}
		matched := false
		for _, line := range strings.Split(info, "\n") {
			if line == "fsxattr.projid = "+strconv.FormatUint(uint64(project), 10) {
				matched = true
			}
		}
		if !matched {
			return zero, errors.New("bench directory project changed")
		}
		if e = os.Remove(a.Path); e != nil {
			return zero, e
		}
	} else if !os.IsNotExist(e) {
		return zero, e
	}
	// Open-but-unlinked files still consume project usage. Do not reclaim their
	// reservation until both blocks and inodes are zero.
	id := strconv.FormatUint(uint64(project), 10)
	for _, unit := range []string{"-b", "-i"} {
		report, e := xfs("/usr/sbin/xfs_quota", "-x", "-c", fmt.Sprintf("report -p -n %s -N -L %s -U %s", unit, id, id), p.Root)
		if e != nil {
			return zero, e
		}
		verified := false
		for _, line := range strings.Split(report, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 4 && fields[0] == "#"+id {
				verified = fields[1] == "0"
			}
		}
		if !verified {
			return zero, errors.New("project retains blocks/inodes or usage cannot be confirmed; reservation retained")
		}
	}
	// Retain the hard limit on the retired project. A crash/retry can still query
	// its zero-usage row; no ID is ever reused, and only logical reservation is freed.
	a.Bytes = 0
	a.Inodes = 0
	l.Assignments["bench:"+key] = a
	if e = save(p.Ledger, filepath.Join(p.Ledger, "assignments.json"), l); e != nil {
		return zero, e
	}
	return a, nil
}

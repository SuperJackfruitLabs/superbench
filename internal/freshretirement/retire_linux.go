//go:build linux

package freshretirement

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/allocationledger"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

// ObserveAbsent reads a Fresh reservation only after its fixed leaf is absent
// and both usage reports uniquely prove zero. It never writes or deletes data.
func ObserveAbsent(ctx context.Context, p hoststorage.Policy, key string) (Approval, error) {
	a, _, err := run(ctx, p, key, nil)
	return a, err
}

// RetireAbsent changes only the selected ledger row to a permanent tombstone.
// It requires direct root; the maintenance owner must hold operations.lock and
// independently establish denial, provenance, quiescence and durable approval.
// There is deliberately no service, sudo or command route to this primitive.
func RetireAbsent(ctx context.Context, p hoststorage.Policy, approved Approval) (hoststorage.Assignment, error) {
	if approved.validate(p) != nil {
		return hoststorage.Assignment{}, ErrRetirement
	}
	_, a, err := run(ctx, p, approved.Key, &approved)
	return a, err
}

func run(ctx context.Context, p hoststorage.Policy, key string, approved *Approval) (Approval, hoststorage.Assignment, error) {
	var zero Approval
	var noAssignment hoststorage.Assignment
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return zero, noAssignment, err
	}
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || p.Validate() != nil || p.OperationJournal != "indexed-v2" || !allocationledger.Key.MatchString(key) {
		return zero, noAssignment, ErrRetirement
	}
	if err := storagecustody.RootOwnedDirectory(p.Ledger, true); err != nil {
		return zero, noAssignment, err
	}
	lockPath := filepath.Join(p.Ledger, "lock")
	lock, err := storagecustody.OpenProtected(lockPath)
	if err != nil {
		return zero, noAssignment, err
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return zero, noAssignment, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return zero, noAssignment, ErrRetirement
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	authority, err := p.OperationAuthority(ctx)
	if err != nil {
		return zero, noAssignment, err
	}
	raw, err := storagecustody.ReadProtected(filepath.Join(p.Ledger, "assignments.json"), 16<<20)
	if err != nil {
		return zero, noAssignment, err
	}
	var original *hoststorage.Assignment
	if approved != nil {
		original = &approved.Assignment
	}
	l, a, retired, err := selected(p, key, raw, original)
	if err != nil {
		return zero, noAssignment, err
	}
	parentPath := filepath.Join(p.Root, "benches")
	if err = storagecustody.RootOwnedDirectory(parentPath, false); err != nil {
		return zero, noAssignment, err
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return zero, noAssignment, err
	}
	defer parent.Close()
	parentInfo, err := parent.Lstat(".")
	if err != nil {
		return zero, noAssignment, err
	}
	st := parentInfo.Sys().(*syscall.Stat_t)
	observed := Approval{Version: Version, Key: key, Authority: authority, Assignment: a, Parent: Parent{uint64(st.Dev), st.Ino, uint32(parentInfo.Mode()), st.Uid, st.Gid}}
	dataDevice, _, err := storagecustody.PhysicalRoot(p.Root)
	if err != nil {
		return zero, noAssignment, err
	}
	if observed.Parent.Device != dataDevice || observed.validate(p) != nil || approved != nil && observed != *approved {
		return zero, noAssignment, ErrRetirement
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		currentAuthority, err := p.OperationAuthority(ctx)
		if err != nil {
			return err
		}
		if currentAuthority != authority {
			return ErrRetirement
		}
		currentLock, err := storagecustody.OpenProtected(lockPath)
		if err != nil {
			return err
		}
		currentLockInfo, err := currentLock.Stat()
		currentLock.Close()
		if err != nil || !os.SameFile(info, currentLockInfo) || parentNode(currentLockInfo) != parentNode(info) {
			return ErrRetirement
		}
		if err := storagecustody.RootOwnedDirectory(parentPath, false); err != nil {
			return err
		}
		named, err := os.Lstat(parentPath)
		actual, e := parent.Lstat(".")
		if err != nil || e != nil || parentNode(named) != observed.Parent || parentNode(actual) != observed.Parent {
			return ErrRetirement
		}
		// A child mount must not hide the real fixed leaf, even on the same
		// filesystem. The benches directory belongs to the dedicated data mount.
		mount, err := fixedOutput(ctx, "/usr/bin/findmnt", "--noheadings", "--raw", "--output", "TARGET", "--target", parentPath)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(mount)) != p.Root {
			return ErrRetirement
		}
		if _, err := parent.Lstat(key); !errors.Is(err, syscall.ENOENT) {
			if err != nil {
				return err
			}
			return ErrRetirement
		}
		return nil
	}
	if err = check(); err != nil {
		return zero, noAssignment, err
	}
	for _, unit := range []string{"-b", "-i"} {
		report, e := quotaReport(ctx, p.Root, a.ProjectID, unit)
		want := a.Inodes
		if unit == "-b" {
			want = a.Bytes / 1024
		}
		if e != nil {
			return zero, noAssignment, e
		}
		if e = unused(report, a.ProjectID, want); e != nil {
			return zero, noAssignment, e
		}
	}
	if err = check(); err != nil {
		return zero, noAssignment, err
	}
	if approved == nil {
		return observed, a, nil
	}
	tombstone := a
	tombstone.Bytes, tombstone.Inodes = 0, 0
	if !retired {
		l.Assignments["bench:"+key] = tombstone
		if err = l.Validate(p.Root, p.AllocationBinding(), false); err != nil {
			return zero, noAssignment, err
		}
		if err = allocationledger.Save(p.Ledger, filepath.Join(p.Ledger, "assignments.json"), l); err != nil {
			return zero, noAssignment, err
		}
	}
	if err = check(); err != nil {
		return zero, noAssignment, err
	}
	return observed, tombstone, nil
}

func parentNode(info os.FileInfo) Parent {
	st := info.Sys().(*syscall.Stat_t)
	return Parent{uint64(st.Dev), st.Ino, uint32(info.Mode()), st.Uid, st.Gid}
}

func quotaReport(ctx context.Context, root string, project uint32, unit string) ([]byte, error) {
	return fixedOutput(ctx, "/usr/sbin/xfs_quota", "-x", "-c", fmt.Sprintf("report -p -n %s -N -L %d -U %d", unit, project, project), root)
}

func fixedOutput(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.WaitDelay = time.Second
	var out reportOutput
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.raw, nil
}

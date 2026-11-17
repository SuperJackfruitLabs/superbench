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
	"golang.org/x/sys/unix"
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
	var original *hoststorage.Assignment
	if approved != nil {
		original = &approved.Assignment
	}
	var observed Approval
	var result hoststorage.Assignment
	err := hold(ctx, p, key, original, func(h held) error {
		if approved != nil && h.observed != *approved {
			return ErrRetirement
		}
		a := h.assignment
		check := func() error {
			if err := h.check(); err != nil {
				return err
			}
			if _, err := h.parent.Lstat(key); !errors.Is(err, syscall.ENOENT) {
				if err != nil {
					return err
				}
				return ErrRetirement
			}
			return nil
		}
		if err := check(); err != nil {
			return err
		}
		for _, unit := range []string{"-b", "-i"} {
			report, e := quotaReport(ctx, p.Root, a.ProjectID, unit)
			want := a.Inodes
			if unit == "-b" {
				want = a.Bytes / 1024
			}
			if e != nil {
				return e
			}
			if e = unused(report, a.ProjectID, want); e != nil {
				return e
			}
		}
		// Leaf and content removal must be durable on the data filesystem before
		// its ENOENT is trusted for a tombstone saved on the ledger filesystem.
		if err := h.syncData(); err != nil {
			return err
		}
		if err := check(); err != nil {
			return err
		}
		if approved == nil {
			observed, result = h.observed, a
			return nil
		}
		tombstone := a
		tombstone.Bytes, tombstone.Inodes = 0, 0
		if !h.retired {
			l := h.ledger
			l.Assignments["bench:"+key] = tombstone
			if err := l.Validate(p.Root, p.AllocationBinding(), false); err != nil {
				return err
			}
			if err := allocationledger.Save(p.Ledger, filepath.Join(p.Ledger, "assignments.json"), l); err != nil {
				return err
			}
		}
		if err := check(); err != nil {
			return err
		}
		observed, result = h.observed, tombstone
		return nil
	})
	if err != nil {
		return zero, noAssignment, err
	}
	return observed, result, nil
}

// held is one selected Fresh row under the allocation lock. check repeats the
// authority, lock, parent and dedicated-mount checks; it does not test the leaf.
type held struct {
	ledger     allocationledger.Ledger
	assignment hoststorage.Assignment
	retired    bool
	observed   Approval
	parent     *os.Root
	check      func() error
	syncData   func() error
}

// hold takes the allocation lock, selects the row through the Fresh rules, pins
// the benches parent on the dedicated data mount and holds the data root open.
func hold(ctx context.Context, p hoststorage.Policy, key string, original *hoststorage.Assignment, action func(held) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || p.Validate() != nil || p.OperationJournal != "indexed-v2" || !allocationledger.Key.MatchString(key) {
		return ErrRetirement
	}
	if err := storagecustody.RootOwnedDirectory(p.Ledger, true); err != nil {
		return err
	}
	lockPath := filepath.Join(p.Ledger, "lock")
	lock, err := storagecustody.OpenProtected(lockPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return ErrRetirement
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	authority, err := p.OperationAuthority(ctx)
	if err != nil {
		return err
	}
	raw, err := storagecustody.ReadProtected(filepath.Join(p.Ledger, "assignments.json"), 16<<20)
	if err != nil {
		return err
	}
	l, a, retired, err := selected(p, key, raw, original)
	if err != nil {
		return err
	}
	parentPath := filepath.Join(p.Root, "benches")
	if err = storagecustody.RootOwnedDirectory(parentPath, false); err != nil {
		return err
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	parentInfo, err := parent.Lstat(".")
	if err != nil {
		return err
	}
	st := parentInfo.Sys().(*syscall.Stat_t)
	observed := Approval{Version: Version, Key: key, Authority: authority, Assignment: a, Parent: Parent{uint64(st.Dev), st.Ino, uint32(parentInfo.Mode()), st.Uid, st.Gid}}
	dataDevice, dataInode, err := storagecustody.PhysicalRoot(p.Root)
	if err != nil {
		return err
	}
	if observed.Parent.Device != dataDevice || observed.validate(p) != nil {
		return ErrRetirement
	}
	data, err := os.OpenFile(p.Root, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer data.Close()
	var dataStat unix.Stat_t
	if err = unix.Fstat(int(data.Fd()), &dataStat); err != nil {
		return err
	}
	if uint64(dataStat.Dev) != dataDevice || dataStat.Ino != dataInode {
		return ErrRetirement
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
		return nil
	}
	syncData := func() error {
		if err := unix.Syncfs(int(data.Fd())); err != nil {
			return &os.PathError{Op: "syncfs", Path: p.Root, Err: err}
		}
		return nil
	}
	return action(held{l, a, retired, observed, parent, check, syncData})
}

// Selection is the selected, unretired Fresh allocation with its pinned benches
// parent and the ledger's filesystem UUID, observed under the allocation lock.
// It is evidence for a recovery step, not authority for any effect.
type Selection struct {
	Approval       Approval
	FilesystemUUID string
}

// Select holds the allocation lock while action runs. It applies the same rules
// as retirement and refuses a retired row. recheck repeats the authority, lock,
// parent and dedicated-mount checks but does not test the leaf; sync runs
// syncfs on the held data root. Direct root only; there is no service route.
func Select(ctx context.Context, p hoststorage.Policy, key string, action func(s Selection, recheck, sync func() error) error) error {
	if action == nil {
		return ErrRetirement
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return hold(ctx, p, key, nil, func(h held) error {
		return action(Selection{Approval: h.observed, FilesystemUUID: h.ledger.FilesystemUUID}, h.check, h.syncData)
	})
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

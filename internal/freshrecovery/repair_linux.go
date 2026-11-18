//go:build linux

package freshrecovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"golang.org/x/sys/unix"
)

const xfsMagic = 0x58465342

// prove establishes every repair precondition for the selected reserved row:
// the leaf is absent below the pinned parent, the data root is the dedicated
// XFS mount with the ledger's UUID, project accounting and enforcement are on,
// and both reports agree on either no row or the exact original caps.
func prove(ctx context.Context, p hoststorage.Policy, sel freshretirement.Selection, key string, recheck func() error) (rows, error) {
	a := sel.Approval.Assignment
	benches, err := openBenches(p.Root, sel.Approval.Parent)
	if err != nil {
		return noRow, err
	}
	defer benches.Close()
	var st unix.Stat_t
	if err = unix.Fstatat(int(benches.Fd()), key, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return noRow, errors.Join(ErrRecovery, err)
	}
	fd, err := unix.Open(p.Root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return noRow, &os.PathError{Op: "open", Path: p.Root, Err: err}
	}
	defer unix.Close(fd)
	var fs unix.Statfs_t
	if err = unix.Fstatfs(fd, &fs); err != nil {
		return noRow, err
	}
	if err = unix.Fstat(fd, &st); err != nil {
		return noRow, err
	}
	if fs.Type != xfsMagic || uint64(st.Dev) != sel.Approval.Parent.Device {
		return noRow, ErrRecovery
	}
	mount, _, err := tool(ctx, "/usr/bin/findmnt", "--noheadings", "--raw", "--output", "TARGET,FSROOT,UUID", "--target", p.Root)
	if err != nil {
		return noRow, err
	}
	if err = mountIdentity(mount, p.Root, sel.FilesystemUUID); err != nil {
		return noRow, err
	}
	state, _, err := tool(ctx, "/usr/sbin/xfs_quota", "-x", "-c", "state -p", p.Root)
	if err != nil {
		return noRow, err
	}
	if err = quotaOn(state); err != nil {
		return noRow, err
	}
	var found [2]rows
	for k, unit := range []string{"-b", "-i"} {
		hard := a.Inodes
		if unit == "-b" {
			hard = a.Bytes / 1024
		}
		id := strconv.FormatUint(uint64(a.ProjectID), 10)
		report, diagnostic, err := tool(ctx, "/usr/sbin/xfs_quota", "-x", "-c", "report -p -n "+unit+" -N -L "+id+" -U "+id, p.Root)
		if err != nil {
			return noRow, err
		}
		if found[k], err = classify(report, diagnostic, a.ProjectID, hard); err != nil {
			return noRow, err
		}
	}
	if found[0] != found[1] {
		return noRow, ErrRecovery
	}
	return found[0], recheck()
}

// PlanRepair proves the repair preconditions for a reserved allocation whose
// leaf is absent and returns the plan for review. It writes nothing.
func PlanRepair(ctx context.Context, m *payloadbootstrap.Manager, s payloadbootstrap.Spec) (RepairPlan, error) {
	var plan RepairPlan
	err := step(ctx, m, s, nil, func(r payloadbootstrap.Recovery, i payloadbootstrap.Inspection, sel freshretirement.Selection, recheck, _ func() error) error {
		a := sel.Approval.Assignment
		plan = RepairPlan{RepairVersion, i, sel.Approval, sel.FilesystemUUID, Limit{a.ProjectID, a.Bytes / 1024, a.Inodes}}
		if err := plan.validate(s, r.Storage); err != nil {
			return err
		}
		_, err := prove(ctx, r.Storage, sel, s.Key, recheck)
		return err
	})
	if err != nil {
		return RepairPlan{}, err
	}
	return plan, nil
}

// Repair retains the reviewed plan, then sets exactly the original hard caps
// with one xfs_quota limit command and reads both reports back. Rows that
// already hold exactly those caps are an idempotent success; any other row
// refuses. It never runs Apply, mkdir, project -s, a chown or an adoption.
func Repair(ctx context.Context, m *payloadbootstrap.Manager, s payloadbootstrap.Spec, plan RepairPlan) (Result, error) {
	raw, err := encode(plan)
	if err != nil {
		return Result{}, err
	}
	retained := false
	err = step(ctx, m, s, &plan.Bootstrap, func(r payloadbootstrap.Recovery, _ payloadbootstrap.Inspection, sel freshretirement.Selection, recheck, sync func() error) error {
		if err := plan.validate(s, r.Storage); err != nil || sel.Approval != plan.Storage || sel.FilesystemUUID != plan.FilesystemUUID {
			return ErrRecovery
		}
		found, err := prove(ctx, r.Storage, sel, s.Key, recheck)
		if err != nil {
			return err
		}
		if err = retain(r.Storage.Ledger, payloadbootstrap.Stem(s), "repair", raw, false); err != nil {
			return err
		}
		retained = true
		if err = r.Gate(); err != nil {
			return err
		}
		if found == noRow {
			l := plan.Limit
			if _, _, err = tool(ctx, "/usr/sbin/xfs_quota", "-x", "-c", limitCommand(l.ProjectID, l.BlockHardKiB*1024, l.InodeHard), r.Storage.Root); err != nil {
				return err
			}
			// The limit is a logged XFS change; commit it before reading it back.
			if err = sync(); err != nil {
				return err
			}
			if err = r.Gate(); err != nil {
				return err
			}
		}
		if found, err = prove(ctx, r.Storage, sel, s.Key, recheck); err != nil {
			return err
		}
		if found != exactRow {
			return ErrRecovery
		}
		return nil
	})
	if err != nil {
		if retained {
			err = fmt.Errorf("effects may have occurred; retry the same plan: %w", err)
		}
		return Result{}, err
	}
	return Result{ResultVersion, s.Key, "repair-bootstrap-quota", digest(raw)}, nil
}

//go:build linux

package freshrecovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cleanworker"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshliveness"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
	"golang.org/x/sys/unix"
)

// reclaiming is what a reclaim established from the retained intent and the
// installed policies before any effect: the bench's allocation, its leaf, the
// benches parent pinned in this boot and the storage authority.
type reclaiming struct {
	r          payloadbootstrap.Reclamation
	i          payloadbootstrap.Inspection
	key, root  string
	assignment hoststorage.Assignment
	parent     freshretirement.Parent
	authority  string
	live       freshliveness.Liveness
}

// Reclaim deletes a released Fresh bench's tree as the payload account, removes
// its empty leaf and retires its quota project, under the reclaim operation's
// reservation (payloadbootstrap.Reclaim). The intent's reclaiming phase is
// retained before any effect; an exact retry after a crash, a killed worker
// or a lost reply continues from what is left and returns the same tombstone.
// Errors after retention say so.
func Reclaim(ctx context.Context, m *payloadbootstrap.Manager, s payloadbootstrap.Spec) (quotahelper.ReclaimResult, error) {
	var result quotahelper.ReclaimResult
	retained := false
	if m == nil {
		return result, ErrRecovery
	}
	err := m.Reclaim(ctx, s, func(r payloadbootstrap.Reclamation) error {
		var err error
		result, err = reclaimAction(ctx, r, s, &retained)
		return err
	})
	if err != nil {
		return quotahelper.ReclaimResult{}, effects(err, retained)
	}
	return result, nil
}

// effects says that a failed reclaim may have had effects once its intent was
// retained, so the operator retries the same reclaim.
func effects(err error, retained bool) error {
	if err != nil && retained {
		return fmt.Errorf("effects may have occurred; retry the same reclaim: %w", err)
	}
	return err
}

// reclaimAction is one reclaim under its reservation. retained is set once
// the intent is, or already was, reclaiming.
func reclaimAction(ctx context.Context, r payloadbootstrap.Reclamation, s payloadbootstrap.Spec, retained *bool) (quotahelper.ReclaimResult, error) {
	var result quotahelper.ReclaimResult
	// A retry of a retained reclaim may follow effects, whatever fails.
	if i, err := r.Inspection(); err == nil && (i.Phase == "reclaiming" || i.Phase == "reclaimed") {
		*retained = true
	}
	c, err := bindReclaim(ctx, r, s)
	if err != nil {
		return result, err
	}
	var tombstone hoststorage.Assignment
	var ran bool
	ran, err = reclaimSteps{
		phase:  func() (string, error) { return c.i.Phase, nil },
		leaf:   c.leafPresent,
		fenced: func() error { return controlsFenced(r.Storage.Ledger, s.Key, c.i) },
		idle:   c.live.Idle,
		retain: func() error { return r.Advance("reclaiming") },
		purge:  func() error { return c.purge(ctx) },
		remove: func() error { return c.active(c.remove(ctx)) },
		retire: func() (err error) { tombstone, err = c.retire(ctx); return c.active(err) },
		finish: func() error { return r.Advance("reclaimed") },
	}.run()
	*retained = *retained || ran
	if err != nil {
		return result, err
	}
	result = quotahelper.ReclaimResult{Version: quotahelper.ReclaimVersion, Key: s.Key, Allocation: tombstone}
	if err = result.Validate(r.Storage.Root, s.Key, c.assignment.ProjectID); err != nil {
		return quotahelper.ReclaimResult{}, errors.Join(ErrRecovery, err)
	}
	return result, r.Check()
}

// bindReclaim checks the retained intent against the spec and the installed
// policies, and pins the benches parent and the storage authority.
func bindReclaim(ctx context.Context, r payloadbootstrap.Reclamation, s payloadbootstrap.Spec) (*reclaiming, error) {
	i, err := r.Inspection()
	if err != nil {
		return nil, err
	}
	spec, err := s.Bytes()
	if err != nil {
		return nil, err
	}
	p, st := r.Payload, r.Storage
	policy, err := p.Bytes()
	if err != nil || i.Version != payloadbootstrap.InspectionVersion || i.Key != s.Key || i.ID != s.ID || i.SpecSHA256 != digest(spec) || i.PolicyDigest != digest(policy) || p.DataRoot != st.Root || p.DaemonUID != uint32(st.OwnerUID) || p.DaemonGID != uint32(st.OwnerGID) {
		return nil, errors.Join(ErrRecovery, err)
	}
	if _, _, err = workerAccount(p, cleanworker.Purge); err != nil {
		return nil, err
	}
	c := &reclaiming{r: r, i: i, key: s.Key, root: filepath.Join(st.Root, "benches", s.Key), assignment: i.RecordedAllocation}
	want := s.QuotaBytes
	if want == 0 {
		want = st.BenchBytes
	}
	a := c.assignment
	if a.Path != c.root || a.ProjectID < 10000 || a.Bytes != want || a.Inodes != st.BenchInodes || i.RecordedRoot.Inode == 0 {
		return nil, ErrRecovery
	}
	if c.authority, err = st.OperationAuthority(ctx); err != nil {
		return nil, err
	}
	if c.authority != i.Operation.Authority {
		return nil, ErrRecovery
	}
	benches := filepath.Join(st.Root, "benches")
	if err = storagecustody.RootOwnedDirectory(benches, false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(benches)
	if err != nil {
		return nil, err
	}
	stat := info.Sys().(*syscall.Stat_t)
	c.parent = freshretirement.Parent{Device: uint64(stat.Dev), Inode: stat.Ino, Mode: uint32(info.Mode()), UID: stat.Uid, GID: stat.Gid}
	c.live = freshliveness.Installed(p.CgroupRoot, p.PayloadUID, c.parent.Device, a.ProjectID)
	return c, nil
}

// active names a failed removal or retirement ErrReclaimPayloadActive when
// the reason is that something still uses the bench: entries reappeared after
// the purge, or an open file keeps usage above zero.
func (c *reclaiming) active(err error) error {
	if err != nil && errors.Is(c.live.Idle(), ErrReclaimBusy) {
		return errors.Join(ErrReclaimPayloadActive, err)
	}
	return err
}

// holdLeaf opens the bench's own leaf below the pinned parent: the recorded
// inode on this boot's data device, a payload-owned directory carrying the
// assigned project. Its mode is the payload's to change. errAbsent: gone.
func (c *reclaiming) holdLeaf() (*os.File, *os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	benches, err := openBenches(c.r.Storage.Root, c.parent)
	if err != nil {
		return nil, nil, st, err
	}
	if err = unix.Fstatat(int(benches.Fd()), c.key, &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		benches.Close()
		return nil, nil, st, errAbsent
	} else if err != nil {
		benches.Close()
		return nil, nil, st, err
	}
	leaf, st, err := openDir(benches, c.key, c.root)
	if err == nil {
		var project uint32
		if project, err = projectID(leaf); err == nil {
			n, p := nodeOf(st), c.r.Payload
			if n.Inode != c.i.RecordedRoot.Inode || n.Device != c.parent.Device || n.Mode&modeType != modeDir || n.UID != p.PayloadUID || n.GID != p.PayloadGID || project != c.assignment.ProjectID {
				err = ErrRecovery
			}
		}
		if err != nil {
			leaf.Close()
		}
	}
	if err != nil {
		benches.Close()
		return nil, nil, st, err
	}
	return benches, leaf, st, nil
}

func (c *reclaiming) leafPresent() (bool, error) {
	benches, leaf, _, err := c.holdLeaf()
	if errors.Is(err, errAbsent) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	leaf.Close()
	benches.Close()
	return true, nil
}

// purge has the payload-uid worker empty the leaf. Root opens none of it.
func (c *reclaiming) purge(ctx context.Context) error {
	target := cleanworker.Target{Device: c.parent.Device, Inode: c.i.RecordedRoot.Inode, ProjectID: c.assignment.ProjectID}
	_, err := runWorker(ctx, c.r.Payload, cleanworker.Request{Version: cleanworker.Version, Mode: cleanworker.Purge, Root: c.root, Target: &target})
	return err
}

// remove deletes the leaf only while it is the bench's own, empty directory,
// under the allocation lock, with the unretired row still the intent's.
func (c *reclaiming) remove(ctx context.Context) error {
	return freshretirement.SelectReclaimed(ctx, c.r.Storage, c.key, func(sel freshretirement.Selection, recheck, _ func() error) error {
		if sel.Approval.Assignment != c.assignment || sel.Approval.Parent != c.parent || sel.Approval.Authority != c.authority {
			return ErrRecovery
		}
		benches, leaf, st, err := c.holdLeaf()
		if err != nil {
			return err
		}
		defer benches.Close()
		defer leaf.Close()
		if err = rmdirAt(benches, leaf, c.key, nodeOf(st)); err != nil {
			return err
		}
		return recheck()
	})
}

// retire proves the leaf gone and usage zero, syncs the data filesystem and
// saves the tombstone; after an earlier tombstone it only proves the same.
func (c *reclaiming) retire(ctx context.Context) (hoststorage.Assignment, error) {
	approval := freshretirement.Approval{Version: freshretirement.Version, Key: c.key, Authority: c.authority, Assignment: c.assignment, Parent: c.parent}
	tombstone, err := freshretirement.RetireReclaimed(ctx, c.r.Storage, approval)
	if err != nil {
		return hoststorage.Assignment{}, err
	}
	if tombstone.Path != c.assignment.Path || tombstone.ProjectID != c.assignment.ProjectID || tombstone.Bytes != 0 || tombstone.Inodes != 0 {
		return hoststorage.Assignment{}, ErrRecovery
	}
	return tombstone, nil
}

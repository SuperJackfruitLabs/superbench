//go:build linux

package freshrecovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cleanworker"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshcleanup"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"golang.org/x/sys/unix"
)

// PlanFile names a published cleanup plan: its path and the SHA-256 of its
// exact bytes, which the operator passes back to clean-bootstrap.
type PlanFile struct{ Path, SHA256 string }

// cleaning is what a cleanup step has established under Recover: the retained
// inspection, the selected allocation and the binding, owner and assignment
// every approval for this leak must carry.
type cleaning struct {
	r          payloadbootstrap.Recovery
	i          payloadbootstrap.Inspection
	sel        freshretirement.Selection
	binding    freshcleanup.Binding
	owners     owners
	assignment freshcleanup.Assignment
	root       string
}

// cleanStep runs action inside Recover (MaintainDenied, direct root) after the
// gate, the retained inspection, the Fresh selection and the Git-control check,
// and ends with the gate, an unchanged inspection and the policy recheck. The
// worker runs outside the allocation lock's short selection window: action
// calls reselect to take it again, compare the selection and recheck.
func cleanStep(ctx context.Context, m *payloadbootstrap.Manager, s payloadbootstrap.Spec, action func(c *cleaning, reselect func(func(sync func() error) error) error) error) error {
	if m == nil || action == nil {
		return ErrRecovery
	}
	return m.Recover(ctx, s, func(r payloadbootstrap.Recovery) error {
		if err := r.Gate(); err != nil {
			return err
		}
		i, err := r.Inspection()
		if err != nil {
			return err
		}
		c := &cleaning{r: r, i: i, root: filepath.Join(r.Storage.Root, "benches", s.Key)}
		reselect := func(inside func(sync func() error) error) error {
			return freshretirement.Select(ctx, r.Storage, s.Key, func(sel freshretirement.Selection, recheck, sync func() error) error {
				if c.sel != (freshretirement.Selection{}) && sel != c.sel {
					return ErrRecovery
				}
				c.sel = sel
				if inside != nil {
					if err := inside(sync); err != nil {
						return err
					}
				}
				return recheck()
			})
		}
		if err = reselect(func(func() error) error { return c.bind(s) }); err != nil {
			return err
		}
		if err = controlsFenced(r.Storage.Ledger, s.Key, i); err != nil {
			return err
		}
		if err = action(c, reselect); err != nil {
			return err
		}
		if err = r.Gate(); err != nil {
			return err
		}
		after, err := r.Inspection()
		if err != nil {
			return err
		}
		if after != i {
			return ErrRecovery
		}
		return r.Check()
	})
}

// bind checks the shared provenance and derives the expected binding,
// assignment and owners from the retained inspection and the selection.
func (c *cleaning) bind(s payloadbootstrap.Spec) error {
	p, i, a := c.r.Payload, c.i, c.sel.Approval
	if !cleanable(i.Phase) || bound(s, c.r.Storage, i, a) != nil {
		return ErrRecovery
	}
	raw, err := p.Bytes()
	if err != nil || digest(raw) != i.PolicyDigest || p.DataRoot != c.r.Storage.Root || p.DaemonUID != uint32(c.r.Storage.OwnerUID) || p.DaemonGID != uint32(c.r.Storage.OwnerGID) {
		return errors.Join(ErrRecovery, err)
	}
	c.binding = freshcleanup.Binding{Key: s.Key, ID: s.ID, PolicySHA256: i.PolicyDigest, SpecSHA256: i.SpecSHA256, IntentSHA256: i.IntentSHA256,
		Operation: i.Operation, RootDevice: i.RecordedRoot.Device, RootInode: i.RecordedRoot.Inode}
	c.owners = owners{p.DaemonUID, p.DaemonGID}
	c.assignment = freshcleanup.Assignment{ProjectID: a.Assignment.ProjectID, Bytes: a.Assignment.Bytes, Inodes: a.Assignment.Inodes}
	return nil
}

// controlsFenced requires KEY.controls to be the recorded root-owned private
// directory and its git-controls journal to be absent, empty or fenced, by
// gitcontrol.Recover's rules, without changing it: the intents stay as evidence.
func controlsFenced(ledger, key string, i payloadbootstrap.Inspection) error {
	state := filepath.Join(ledger, "payload-bootstrap", key+".controls")
	info, err := os.Lstat(state)
	if err != nil {
		return errors.Join(ErrRecovery, err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || st.Uid != 0 || info.Mode().Perm() != 0700 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || uint64(st.Dev) != i.RecordedControls.Device || st.Ino != i.RecordedControls.Inode {
		return ErrRecovery
	}
	if err = gitcontrol.Fenced(state); err != nil {
		return errors.Join(ErrRecovery, err)
	}
	return nil
}

// holdLeaf opens the leaf below the pinned benches parent and derives the owner
// from the phase. errAbsent means the leaf is gone.
func (c *cleaning) holdLeaf(key string) (*os.File, Node, freshcleanup.Owner, error) {
	benches, err := openBenches(c.r.Storage.Root, c.sel.Approval.Parent)
	if err != nil {
		return nil, Node{}, freshcleanup.Owner{}, err
	}
	defer benches.Close()
	var st unix.Stat_t
	if err = unix.Fstatat(int(benches.Fd()), key, &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil, Node{}, freshcleanup.Owner{}, errAbsent
	} else if err != nil {
		return nil, Node{}, freshcleanup.Owner{}, err
	}
	leaf, st, err := openDir(benches, key, c.root)
	if err != nil {
		return nil, Node{}, freshcleanup.Owner{}, err
	}
	project, err := projectID(leaf)
	if err == nil {
		recorded := [2]uint64{c.i.RecordedRoot.Device, c.i.RecordedRoot.Inode}
		var owner freshcleanup.Owner
		if owner, err = cleanOwner(c.i.Phase, recorded, c.assignment.ProjectID, c.owners, c.sel.Approval.Parent.Device, nodeOf(st), project); err == nil {
			return leaf, nodeOf(st), owner, nil
		}
	}
	leaf.Close()
	return nil, Node{}, freshcleanup.Owner{}, err
}

// unchanged requires the held leaf to be the same node by fd and by name.
func (c *cleaning) unchanged(leaf *os.File, want Node) error {
	var st, named unix.Stat_t
	if err := unix.Fstat(int(leaf.Fd()), &st); err != nil {
		return err
	}
	if err := unix.Lstat(c.root, &named); err != nil {
		return err
	}
	if nodeOf(st) != want || nodeOf(named) != want {
		return ErrRecovery
	}
	return nil
}

// parent is the benches parent as the selection pinned it.
func (c *cleaning) parent() freshcleanup.Parent {
	p := c.sel.Approval.Parent
	return freshcleanup.Parent{Device: p.Device, Inode: p.Inode, Mode: p.Mode, UID: p.UID, GID: p.GID}
}

// observation turns a worker reply into the model's observation, with the
// pinned parent. The reply is data: the model validates all of it.
func (c *cleaning) observation(reply cleanworker.Reply) freshcleanup.Observation {
	o := freshcleanup.Observation{Parent: c.parent(), Tree: *reply.Tree}
	for _, v := range reply.Owners {
		o.Owners = append(o.Owners, freshcleanup.Ownership{Path: v.Path, Device: v.Device, Inode: v.Inode, Nlink: v.Nlink, UID: v.UID, GID: v.GID})
	}
	return o
}

// PlanClean observes a leaked daemon-owned tree through the daemon-uid worker,
// validates the reply as a cleanup approval and publishes it as
// LEDGER/payload-cleanups/KEY.plan.json. It deletes nothing and retains no
// approval. The plan may be replaced until clean-bootstrap retains KEY.json.
func PlanClean(ctx context.Context, m *payloadbootstrap.Manager, s payloadbootstrap.Spec) (PlanFile, error) {
	var plan PlanFile
	err := cleanStep(ctx, m, s, func(c *cleaning, reselect func(func(func() error) error) error) error {
		leaf, node, owner, err := c.holdLeaf(s.Key)
		if errors.Is(err, errAbsent) {
			return ErrRecovery
		}
		if err != nil {
			return err
		}
		defer leaf.Close()
		reply, err := runWorker(ctx, c.r.Payload, cleanworker.Request{Version: cleanworker.Version, Mode: cleanworker.Observe, Root: c.root})
		if err != nil {
			return err
		}
		approval, err := freshcleanup.New(c.r.Payload, c.binding, owner, c.assignment, c.observation(reply))
		if err != nil {
			return err
		}
		raw, err := approval.Bytes(c.r.Payload, c.binding, owner, c.assignment)
		if err != nil {
			return err
		}
		if err = c.unchanged(leaf, node); err != nil {
			return err
		}
		return reselect(func(func() error) error {
			dir, err := cleanups(c.r.Storage.Ledger)
			if err != nil {
				return err
			}
			if err = replacePlan(dir, s.Key, raw, 0, freshcleanup.MaxApproval); err != nil {
				return err
			}
			plan = PlanFile{filepath.Join(dir, planName(s.Key)), digest(raw)}
			return nil
		})
	})
	if err != nil {
		return PlanFile{}, err
	}
	return plan, nil
}

// Clean retains the reviewed plan, matched by planSHA, as KEY.json, then has
// the daemon-uid worker delete exactly the approved contents and leave the
// leaf, re-observes the leaf as empty and unchanged, and fsyncs it. A tree that
// differs from the plan is refused before anything is retained. A retry after a
// lost reply, a crash or a killed worker succeeds with the same retained
// approval, including after the leaf itself is gone. Errors after retention may
// follow effects.
func Clean(ctx context.Context, m *payloadbootstrap.Manager, s payloadbootstrap.Spec, planSHA string) (Result, error) {
	if len(planSHA) != 64 || !isHex(planSHA) {
		return Result{}, ErrRecovery
	}
	retained := false
	err := cleanStep(ctx, m, s, func(c *cleaning, reselect func(func(func() error) error) error) error {
		dir := filepath.Join(c.r.Storage.Ledger, "payload-cleanups")
		raw, already, err := reviewed(dir, s.Key, planSHA, 0, freshcleanup.MaxApproval)
		if err != nil {
			return err
		}
		daemon := freshcleanup.Owner{UID: c.owners.daemonUID, GID: c.owners.daemonGID}
		approval, err := freshcleanup.Parse(raw, c.r.Payload, c.binding, daemon, c.assignment)
		if err != nil || approval.Observation.Parent != c.parent() {
			return errors.Join(ErrRecovery, err)
		}
		leaf, node, owner, err := c.holdLeaf(s.Key)
		if errors.Is(err, errAbsent) {
			// Leaf removal followed an earlier clean: only our retained approval
			// makes this a retry.
			if !already {
				return ErrRecovery
			}
			if err = reselect(func(func() error) error {
				return retainFile(c.r.Storage.Ledger, approvalName(s.Key), raw, true, freshcleanup.MaxApproval)
			}); err != nil {
				return err
			}
			retained = true
			return nil
		}
		if err != nil {
			return err
		}
		defer leaf.Close()
		root := approval.Observation.Tree.Root
		if owner != daemon || node.Device != root.Device || node.Inode != root.Inode {
			return ErrRecovery
		}
		if !already {
			// Nothing is retained for a tree that changed since review: the
			// operator plans again instead of holding a plan that cannot apply.
			reply, err := runWorker(ctx, c.r.Payload, cleanworker.Request{Version: cleanworker.Version, Mode: cleanworker.Observe, Root: c.root})
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(c.observation(reply), approval.Observation) {
				return ErrRecovery
			}
		}
		if err = c.unchanged(leaf, node); err != nil {
			return err
		}
		if err = reselect(func(func() error) error {
			return retainFile(c.r.Storage.Ledger, approvalName(s.Key), raw, false, freshcleanup.MaxApproval)
		}); err != nil {
			return err
		}
		retained = true
		if err = c.r.Gate(); err != nil {
			return err
		}
		approved := approval.Observation.Tree
		if _, err = runWorker(ctx, c.r.Payload, cleanworker.Request{Version: cleanworker.Version, Mode: cleanworker.Delete, Root: c.root, Approved: &approved}); err != nil {
			return err
		}
		if err = c.r.Gate(); err != nil {
			return err
		}
		reply, err := runWorker(ctx, c.r.Payload, cleanworker.Request{Version: cleanworker.Version, Mode: cleanworker.Observe, Root: c.root})
		if err != nil {
			return err
		}
		after := reply.Tree.Root
		if len(reply.Tree.Entries) != 0 || after.Device != root.Device || after.Inode != root.Inode || after.Mode != root.Mode || reply.Owners[0].UID != daemon.UID || reply.Owners[0].GID != daemon.GID {
			return ErrRecovery
		}
		if err = c.unchanged(leaf, node); err != nil {
			return err
		}
		// The unlinks are committed through the ordered XFS log with the leaf.
		if err = unix.Fsync(int(leaf.Fd())); err != nil {
			return &os.PathError{Op: "fsync", Path: c.root, Err: err}
		}
		return reselect(nil)
	})
	if err != nil {
		if retained {
			err = fmt.Errorf("effects may have occurred; retry the same plan: %w", err)
		}
		return Result{}, err
	}
	return Result{ResultVersion, s.Key, "clean-bootstrap", planSHA}, nil
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

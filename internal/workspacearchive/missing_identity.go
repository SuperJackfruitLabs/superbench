package workspacearchive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

type missingMember struct {
	Name, IntegrationBase string
	Present               *worktreetransfer.Plan
	Missing               *worktreetransfer.MissingPlan
}
type missingApproval struct {
	Version, Root, DataRoot, ID, SourceKey, SourceID, TargetKey, TargetID, CreationID, Digest string
	Checkpoint                                                                                *model.WorkspaceCheckpoint
	Storage                                                                                   hoststorage.StorageIdentity
	Recovery                                                                                  model.WorkspaceTransferRecovery
	Members                                                                                   []missingMember
}

func (m Manager) missingApproval(tx *sql.Tx, b *model.Bench) (missingApproval, error) {
	var p missingApproval
	j, e := m.transferRecord(tx, b)
	if e != nil {
		return p, e
	}
	if e = json.Unmarshal(j.Plan, &p); e != nil {
		return p, e
	}
	t := b.Workspace.Transfer
	if j.Phase != "completed" || p.Version != "superbench.workspace-missing-transfer.v1" || p.Root != m.State || p.DataRoot != m.Data || p.ID != j.ID || p.Digest != j.Digest || p.SourceKey != b.Key || p.SourceID != b.Workspace.ID || p.TargetKey != t.TargetKey || p.TargetID != t.TargetID || p.CreationID != t.CreationID || !reflect.DeepEqual(p.Checkpoint, b.Workspace.Checkpoint) || !reflect.DeepEqual(&p.Recovery, t.Recovery) || len(p.Members) != len(b.Workspace.Members) {
		return p, errors.New("missing archive transfer identity changed")
	}
	for i, v := range p.Members {
		member := b.Workspace.Members[i]
		if v.Name != member.Name || (v.Present == nil) == (v.Missing == nil) {
			return p, errors.New("missing archive member vector changed")
		}
	}
	return p, nil
}
func (m Manager) missingCheckout(ctx context.Context, tx *sql.Tx, b *model.Bench) error {
	p, e := m.missingApproval(tx, b)
	if e != nil {
		return e
	}
	if e = m.confirm(ctx, b); e != nil {
		return e
	}
	if e = (hoststorage.Worker{State: m.State, Data: m.Data}).VerifyStorage(ctx, p.Storage); e != nil {
		return e
	}
	for i, v := range p.Members {
		member := b.Workspace.Members[i]
		if v.Missing != nil {
			if e = (worktreetransfer.MissingManager{Git: m.Git, Timeout: m.Timeout}).VerifyQuarantine(ctx, *v.Missing); e != nil {
				return e
			}
			if _, e = os.Lstat(member.Path); !os.IsNotExist(e) {
				return errors.New("missing source member reappeared")
			}
			continue
		}
		old := v.Present
		if old.Checkout != member.Path || old.Common != member.Mirror || old.Commit != b.Workspace.Checkpoint.Members[i].Commit || old.TaskRef != "refs/heads/"+member.Branch {
			return errors.New("present source member identity changed")
		}
		if e = originIdentity(member.Path, old.CheckoutIdentity); e != nil {
			return e
		}
		if e = originIdentity(old.Common, old.CommonIdentity); e != nil {
			return e
		}
		if e = originIdentity(old.Admin, old.AdminIdentity); e != nil {
			return e
		}
		if e = originFile(filepath.Join(member.Path, ".git"), old.GitFile); e != nil {
			return e
		}
		for _, entry := range old.Metadata {
			if entry.Path == "gitdir" || entry.Path == "commondir" || entry.Path == "common/config" {
				base := old.Admin
				if entry.Path == "common/config" {
					base = old.Common
					entry.Path = "config"
				}
				if e = originFile(filepath.Join(base, entry.Path), entry); e != nil {
					return e
				}
			}
		}

		head, e := registrationFile(filepath.Join(old.Admin, "HEAD"))
		if e != nil || head != old.Commit+"\n" {
			return errors.New("source detached HEAD changed")
		}
		if b.Workspace.Archive != nil {
			freeze, e := m.readArchiveFreeze(b)
			if e != nil {
				return e
			}
			found := false
			for _, r := range freeze.missing.Present {
				if r.Plan.Spec.Checkout == member.Path {
					found = true
					if r.Plan.Admin != old.Admin {
						return errors.New("present retained admin changed")
					}
					if e = (worktreetransfer.MissingManager{Git: m.Git, Timeout: m.Timeout}).VerifyPresentRetention(ctx, r, b.Workspace.Archive.FrozenAt != ""); e != nil {
						return e
					}
				}
			}
			if !found {
				return errors.New("present source lacks retention approval")
			}
		} else {
			if _, e = registrationIdentity(member, old.Admin); e != nil {
				return e
			}
		}

	}
	return ctx.Err()
}

// checkMissingRoot validates the surviving fixed parent even when the reviewed
// root is absent. Reaping can remove only the exact frozen root, never adopt one.
// For managed storage the freeze's device numbers are not compared, because a
// reboot can renumber the data device: parent and root must be on the data
// root's device in this boot, and the storage identity binds that volume by its
// UUID. Unmanaged storage has no UUID, so its recorded devices are compared.
// This helps only a freeze whose root is absent: a present root's frozen reap
// tree still pins st_dev and refuses after a renumbering (see "Reboot and
// device numbers" in docs/pilot-installation.md).
func checkMissingRoot(p MissingFreeze, managed, reaping bool) error {
	parent := filepath.Dir(p.Path)
	physical, e := filepath.EvalSymlinks(parent)
	if e != nil || physical != parent {
		return errors.New("missing archive parent changed")
	}
	parentDevice, rootDevice := p.Root.ParentDevice, p.Root.Device
	if managed {
		data, e := os.Lstat(filepath.Dir(parent))
		if e != nil {
			return e
		}
		parentDevice = uint64(data.Sys().(*syscall.Stat_t).Dev)
		rootDevice = parentDevice
	}
	st, e := os.Lstat(parent)
	if e != nil {
		return e
	}
	id := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || uint64(id.Dev) != parentDevice || uint64(id.Ino) != p.Root.ParentInode || uint32(st.Mode()) != p.Root.ParentMode {
		return errors.New("missing archive parent identity changed")
	}
	st, e = os.Lstat(p.Path)
	if os.IsNotExist(e) {
		if p.Root.Absent || reaping {
			return nil
		}
		return errors.New("unreviewed missing archive root loss")
	}
	if e != nil {
		return e
	}
	if p.Root.Absent {
		return errors.New("explicitly absent source root reappeared")
	}
	id = st.Sys().(*syscall.Stat_t)
	mode := uint32(st.Mode())
	readonly := p.Root.Mode &^ 0222
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || uint64(id.Dev) != rootDevice || uint64(id.Ino) != p.Root.Inode || mode != p.Root.Mode && mode != readonly && (!reaping || mode != readonly|0200) {
		return errors.New("missing archive root identity changed")
	}
	for _, name := range p.Missing {
		if _, e = os.Lstat(filepath.Join(p.Path, name)); !os.IsNotExist(e) {
			return errors.New("missing member path reappeared")
		}
	}
	return nil
}
func (m Manager) verifyMissingArchive(ctx context.Context, tx *sql.Tx, b *model.Bench) error {
	f, e := m.readArchiveFreeze(b)
	if e != nil {
		return e
	}
	if b.Workspace.Archive.ReapingDigest != "" {
		started, removeErr := m.rootRemovalStarted(b)
		if removeErr != nil {
			return removeErr
		}
		if b.Workspace.Phase == "reaped" && !started {
			return errors.New("reaped missing source lacks root-removal journal")
		}
	}
	if f.missing == nil {
		return errors.New("missing archive lacks explicit intent")
	}
	p, e := m.missingApproval(tx, b)
	if e != nil {
		return e
	}
	if e = checkMissingRoot(*f.missing, p.Storage.Managed, b.Workspace.Phase == "reaping" || b.Workspace.Phase == "reaped"); e != nil {
		return e
	}
	if e = m.confirm(ctx, b); e != nil {
		return e
	}
	for _, v := range p.Members {
		if v.Missing != nil {
			if e = (worktreetransfer.MissingManager{Git: m.Git, Timeout: m.Timeout}).VerifyQuarantine(ctx, *v.Missing); e != nil {
				return e
			}
		}
	}
	for _, retention := range f.missing.Present {
		if e = (worktreetransfer.MissingManager{Git: m.Git, Timeout: m.Timeout}).VerifyPresentRetention(ctx, retention, b.Workspace.Archive.FrozenAt != ""); e != nil {
			return e
		}
	}
	if b.Workspace.Phase == "reaped" {
		if _, e = os.Lstat(b.Path); !os.IsNotExist(e) {
			return errors.New("reaped source path reappeared")
		}
		rp, e := readPlanFile(m.reapJournal(b.Key))
		if e != nil {
			return e
		}
		if rp.Missing == nil {
			return errors.New("missing reap proof absent")
		}
		return m.verifyRetired(ctx, p.Storage, *rp.Missing)
	}
	e = (hoststorage.Worker{State: m.State, Data: m.Data}).VerifyStorage(ctx, p.Storage)
	if e != nil && b.Workspace.Phase == "reaping" {
		if _, absentErr := os.Lstat(b.Path); os.IsNotExist(absentErr) {
			rp, readErr := readPlanFile(m.reapJournal(b.Key))
			if readErr != nil {
				return readErr
			}
			if rp.Missing == nil {
				return errors.New("missing reap approval absent")
			}
			return m.verifyRetired(ctx, p.Storage, *rp.Missing)
		}
	}
	return e
}
func (m Manager) retirement(p missingApproval, f MissingFreeze) hoststorage.RetirementApproval {
	root := f.Root
	if !root.Absent {
		root.Mode = root.Mode&^0222 | 0200
	}
	return hoststorage.RetirementApproval{Version: hoststorage.RetirementVersion, Key: f.Key, Storage: p.Storage, Root: root}
}
func (m Manager) verifyRetired(ctx context.Context, s hoststorage.StorageIdentity, f MissingFreeze) error {
	if s.Managed {
		a := m.retirement(missingApproval{Storage: s}, f)
		if m.VerifyMissingQuota != nil {
			return m.VerifyMissingQuota(ctx, a)
		}
		return (hoststorage.Worker{State: m.State, Data: m.Data}).VerifyRetiredStorage(ctx, a)
	}
	if _, e := os.Lstat(f.Path); !os.IsNotExist(e) {
		return errors.New("retired source path reappeared")
	}
	return checkMissingRoot(f, false, true)
}

func (m Manager) retireQuota(ctx context.Context, a hoststorage.RetirementApproval) error {
	if m.RetireMissingQuota != nil {
		return m.RetireMissingQuota(ctx, a)
	}
	return (hoststorage.Worker{State: m.State, Data: m.Data}).RetireStorage(ctx, a)
}

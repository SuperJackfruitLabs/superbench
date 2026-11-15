package workspacearchive

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reftransfer"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

type transferApproval struct {
	Members []struct {
		Name     string
		Worktree worktreetransfer.Plan
	}
}

func (m Manager) transferRecord(tx *sql.Tx, b *model.Bench) (reftransfer.Record, error) {
	if tx == nil {
		return reftransfer.Record{}, errors.New("transfer archive requires journal transaction")
	}
	t := b.Workspace.Transfer
	j, err := reftransfer.Get(tx, t.ID)
	if err != nil {
		return j, err
	}
	if j.ID != t.ID || j.Digest != t.Digest || j.SourceID != b.Workspace.ID || j.SourceKey != b.Key || j.TargetKey != t.TargetKey || j.TargetID != t.TargetID || j.CreationID != t.CreationID || j.Phase != "relinquished" && j.Phase != "completed" {
		return j, errors.New("source transfer journal changed or incomplete")
	}
	return j, nil
}
func (m Manager) transferJournal(tx *sql.Tx, b *model.Bench) error {
	if b.Workspace.Transfer == nil {
		return nil
	}
	_, err := m.transferRecord(tx, b)
	return err
}
func originIdentity(path string, want worktreetransfer.Identity) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(st.Dev) != want.Device || uint64(st.Ino) != want.Inode || !info.IsDir() {
		return errors.New("transferred source container replaced")
	}
	return nil
}
func (m Manager) transferOrigin(tx *sql.Tx, b *model.Bench) error {
	if b.Workspace.Transfer == nil {
		return nil
	}
	j, err := m.transferRecord(tx, b)
	if err != nil {
		return err
	}
	var approval transferApproval
	if err = json.Unmarshal(j.Plan, &approval); err != nil {
		return err
	}
	if len(approval.Members) != len(b.Workspace.Members) {
		return errors.New("transfer approval member inventory changed")
	}
	for i, member := range b.Workspace.Members {
		old := approval.Members[i]
		p := old.Worktree
		checkout, err := filepath.EvalSymlinks(member.Path)
		if err != nil {
			return err
		}
		mirror, err := filepath.EvalSymlinks(member.Mirror)
		if err != nil {
			return err
		}
		if old.Name != member.Name || p.Version != worktreetransfer.Version || p.Checkout != checkout || p.Common != mirror || p.TaskRef != "refs/heads/"+member.Branch || p.Commit != b.Workspace.Checkpoint.Members[i].Commit || p.ObjectFormat != member.ObjectFormat || filepath.Dir(p.Admin) != filepath.Join(mirror, "worktrees") {
			return errors.New("transfer approval checkout identity changed")
		}
		for path, id := range map[string]worktreetransfer.Identity{checkout: p.CheckoutIdentity, mirror: p.CommonIdentity, p.Admin: p.AdminIdentity} {
			if err = originIdentity(path, id); err != nil {
				return err
			}
		}
		if p.GitFile.Path != ".git" {
			return errors.New("transfer approval lacks gitfile identity")
		}
		if err = originFile(filepath.Join(checkout, ".git"), p.GitFile); err != nil {
			return err
		}
		for _, name := range []string{"gitdir", "commondir"} {
			found := false
			for _, entry := range p.Metadata {
				if entry.Path == name {
					if found {
						return errors.New("duplicate transfer registration metadata")
					}
					found = true
					if err = originFile(filepath.Join(p.Admin, name), entry); err != nil {
						return err
					}
				}
			}
			if !found {
				return errors.New("missing transfer registration metadata")
			}
		}
	}
	return nil
}
func originFile(path string, want worktreetransfer.Entry) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || uint64(st.Dev) != want.Identity.Device || uint64(st.Ino) != want.Identity.Inode {
		return errors.New("transferred source registration file replaced")
	}
	raw, err := registrationFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(raw))
	if info.Size() != want.Size || hex.EncodeToString(sum[:]) != want.Digest {
		return errors.New("transferred source registration bytes changed")
	}
	return nil
}

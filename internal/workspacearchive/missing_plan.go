package workspacearchive

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func (m Manager) planMissing(ctx context.Context, tx *sql.Tx, b *model.Bench) (*Plan, error) {
	a := b.Workspace.Archive
	if b.Workspace.Phase != "archived" || a == nil || a.FrozenAt == "" || a.ReapingDigest != "" {
		return nil, errors.New("missing source reap requires a frozen archive")
	}
	if e := m.missingCheckout(ctx, tx, b); e != nil {
		return nil, e
	}
	f, e := m.readArchiveFreeze(b)
	if e != nil {
		return nil, e
	}
	if e = checkMissingRoot(*f.missing, false); e != nil {
		return nil, e
	}
	var tree reap.Tree
	if !f.missing.Root.Absent {
		tree, e = reap.Frozen(b.Path, f.Tree)
		if e != nil {
			return nil, e
		}
	}
	objects := map[string]string{}
	for i, member := range b.Workspace.Members {
		out, e := m.Git(ctx, member.Mirror, "ls-tree", "-r", "-z", b.Workspace.Checkpoint.Members[i].Commit)
		if e != nil {
			return nil, e
		}
		for _, row := range strings.Split(out, "\x00") {
			header, path, ok := strings.Cut(row, "\t")
			fields := strings.Fields(header)
			if ok && len(fields) == 3 && fields[1] == "blob" {
				objects[filepath.Join(member.Name, path)] = fields[2]
			}
		}
	}
	for i := range tree.Entries {
		v := &tree.Entries[i]
		oid, ok := objects[v.Path]
		v.Recoverable = ok && (oid == v.GitSHA1 || oid == v.GitSHA256)
	}
	approval, e := m.missingApproval(tx, b)
	if e != nil {
		return nil, e
	}
	c := workspace.Clone(b)
	p := &Plan{Version: MissingPlanVersion, Key: b.Key, Path: b.Path, BenchID: b.Workspace.ID, Checkpoint: c.Workspace.Checkpoint, Members: c.Workspace.Members, Transfer: c.Workspace.Transfer, FreezeDigest: a.FreezeDigest, ReapAfter: a.ReapAfter, Tree: tree, Missing: f.missing}
	retirement := m.retirement(approval, *f.missing)
	if approval.Storage.Managed {
		p.Retirement = &retirement
	}
	p.Digest, e = p.DigestValue()
	if e != nil {
		return nil, e
	}
	_, e = p.Bytes()
	return p, e
}

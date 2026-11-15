package workspacearchive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

const PlanVersion = "superbench.workspace-reap.v1"

// Plan is explicit local approval of one frozen aggregate tree, including bytes
// that are not recoverable from Git. It grants no remote-ref deletion authority.
type Plan struct {
	Version, Key, Path, BenchID string
	Checkpoint                  *model.WorkspaceCheckpoint
	Members                     []model.WorkspaceMember
	Registrations               []Registration           `json:",omitempty"`
	Transfer                    *model.WorkspaceTransfer `json:",omitempty"`
	FreezeDigest, ReapAfter     string
	Retirement                  *hoststorage.RetirementApproval `json:",omitempty"`
	Missing                     *MissingFreeze                  `json:",omitempty"`
	Tree                        reap.Tree
	Digest                      string
}

func (p Plan) DigestValue() (string, error) {
	p.Digest = ""
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (p Plan) Bytes() ([]byte, error) {
	raw, err := json.Marshal(p)
	if err == nil && len(raw) > reap.MaxManifest {
		err = errors.New("aggregate reap plan exceeds limit")
	}
	return raw, err
}

// ReadPlan rejects unknown/duplicate fields and ambiguous or oversized input.
func ReadPlan(raw []byte) (*Plan, error) {
	if err := reap.CheckJSON(raw); err != nil {
		return nil, err
	}
	var p Plan
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return nil, err
	}
	digest, err := p.DigestValue()
	if err != nil {
		return nil, err
	}
	if (p.Version != PlanVersion && p.Version != MissingPlanVersion) || !keyPattern.MatchString(p.Key) || p.Digest != digest {
		return nil, errors.New("invalid aggregate reap approval")
	}
	if (p.Version == MissingPlanVersion) != (p.Missing != nil) {
		return nil, errors.New("missing reap approval version differs")
	}
	return &p, nil
}
func readPlanFile(path string) (*Plan, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > reap.MaxManifest {
		return nil, errors.New("invalid aggregate reap journal")
	}
	raw, err := io.ReadAll(io.LimitReader(f, reap.MaxManifest+1))
	if err != nil {
		return nil, err
	}
	return ReadPlan(raw)
}
func matches(p *Plan, b *model.Bench) bool {
	a := b.Workspace.Archive
	return a != nil && p.Key == b.Key && p.Path == b.Path && p.BenchID == b.Workspace.ID && reflect.DeepEqual(p.Checkpoint, b.Workspace.Checkpoint) && reflect.DeepEqual(p.Members, b.Workspace.Members) && reflect.DeepEqual(p.Transfer, b.Workspace.Transfer) && p.FreezeDigest == a.FreezeDigest && p.ReapAfter == a.ReapAfter
}
func (m Manager) plan(ctx context.Context, tx *sql.Tx, b *model.Bench) (*Plan, error) {
	if err := m.available(b); err != nil {
		return nil, err
	}
	if missingSource(b) {
		return m.planMissing(ctx, tx, b)
	}
	a := b.Workspace.Archive
	if b.Workspace.Phase != "archived" || a == nil || a.FrozenAt == "" || a.ReapingDigest != "" {
		return nil, errors.New("aggregate reap planning requires a completed read-only archive")
	}
	if err := m.checkout(ctx, tx, b); err != nil {
		return nil, err
	}
	freeze, err := reap.ReadFreeze(m.freezeJournal(b.Key), b.Key, b.Path, b.Workspace.Checkpoint.Seal, a.FreezeDigest)
	if err != nil {
		return nil, err
	}
	tree, err := reap.Frozen(b.Path, freeze.Tree)
	if err != nil {
		return nil, err
	}
	objects := map[string]string{}
	for _, member := range b.Workspace.Members {
		out, err := m.Git(ctx, member.Path, "ls-tree", "-r", "-z", "HEAD")
		if err != nil {
			return nil, err
		}
		for _, row := range strings.Split(out, "\x00") {
			header, path, ok := strings.Cut(row, "\t")
			if !ok {
				continue
			}
			fields := strings.Fields(header)
			if len(fields) == 3 && fields[1] == "blob" {
				objects[filepath.Join(member.Name, path)] = fields[2]
			}
		}
	}
	for i := range tree.Entries {
		e := &tree.Entries[i]
		oid, ok := objects[e.Path]
		e.Recoverable = ok && (oid == e.GitSHA1 || oid == e.GitSHA256)
	}
	registrations, err := m.registrationSnapshot(ctx, b)
	if err != nil {
		return nil, err
	}
	clone := workspace.Clone(b)
	p := &Plan{Version: PlanVersion, Key: b.Key, Path: b.Path, BenchID: b.Workspace.ID, Checkpoint: clone.Workspace.Checkpoint, Members: clone.Workspace.Members, Registrations: registrations, Transfer: clone.Workspace.Transfer, FreezeDigest: a.FreezeDigest, ReapAfter: a.ReapAfter, Tree: tree}
	p.Digest, err = p.DigestValue()
	if err != nil {
		return nil, err
	}
	if _, err = p.Bytes(); err != nil {
		return nil, err
	}
	return p, nil
}
func (m Manager) PlanReap(ctx context.Context, key string) (*Plan, error) {
	ctx, cancel, err := m.bounded(ctx, key)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var p *Plan
	err = m.Within(func(update workspace.Update) error {
		return update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			var err error
			p, err = m.plan(ctx, tx, rows[key])
			return err
		})
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

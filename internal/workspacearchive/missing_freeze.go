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
	"reflect"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

const MissingPlanVersion = "superbench.workspace-missing-reap.v1"
const missingFreezeVersion = "superbench.workspace-missing-freeze.v1"

// MissingFreeze binds actual surviving bytes, or explicit root absence. Missing
// member names never become deletion authority over paths that later reappear.
type MissingFreeze struct {
	Version, Key, Path, BenchID string
	Checkpoint                  *model.WorkspaceCheckpoint
	Transfer                    *model.WorkspaceTransfer
	Root                        hoststorage.RetirementRoot
	Present                     []worktreetransfer.PresentRetention
	Missing                     []string
	Tree                        reap.Tree
	Digest                      string
}

func missingSource(b *model.Bench) bool {
	return b != nil && b.Workspace != nil && b.Workspace.Transfer != nil && b.Workspace.Transfer.Recovery != nil && b.Workspace.Transfer.Recovery.Mode == "missing"
}
func (p MissingFreeze) digest() (string, error) {
	p.Digest = ""
	raw, err := json.Marshal(p)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), err
}
func (p MissingFreeze) Bytes() ([]byte, error) {
	raw, err := json.Marshal(p)
	if len(raw) > reap.MaxManifest {
		return nil, errors.New("missing archive intent exceeds bound")
	}
	return raw, err
}

type archiveFreeze struct {
	Tree     reap.Tree
	Digest   string
	ordinary *reap.FreezePlan
	missing  *MissingFreeze
	// managed is known only for a freeze captured in this process; a freeze
	// read back from its file compares its recorded devices.
	managed bool
}

func (f archiveFreeze) Bytes() ([]byte, error) {
	if f.missing != nil {
		return f.missing.Bytes()
	}
	return f.ordinary.Bytes()
}
func (f archiveFreeze) preflight(path string) error {
	if f.missing != nil && f.missing.Root.Absent {
		return checkMissingRoot(*f.missing, f.managed, false)
	}
	return reap.FreezePreflight(path, f.Tree)
}
func (f archiveFreeze) apply(path string) error {
	if f.missing != nil && f.missing.Root.Absent {
		return checkMissingRoot(*f.missing, f.managed, false)
	}
	return reap.Freeze(path, f.Tree)
}
func (m Manager) newArchiveFreeze(ctx context.Context, tx *sql.Tx, b *model.Bench) (archiveFreeze, error) {
	if !missingSource(b) {
		p, e := reap.NewFreeze(b.Key, b.Path, b.Workspace.Checkpoint.Seal)
		if e != nil {
			return archiveFreeze{}, e
		}
		return archiveFreeze{Tree: p.Tree, Digest: p.Digest, ordinary: p}, nil
	}
	j, e := m.missingApproval(tx, b)
	if e != nil {
		return archiveFreeze{}, e
	}
	root, e := hoststorage.CaptureRetirementRoot(j.Storage)
	if e != nil {
		return archiveFreeze{}, e
	}
	c := workspace.Clone(b)
	p := &MissingFreeze{Version: missingFreezeVersion, Key: b.Key, Path: b.Path, BenchID: b.Workspace.ID, Checkpoint: c.Workspace.Checkpoint, Transfer: c.Workspace.Transfer, Root: root}
	for _, v := range j.Members {
		if v.Missing != nil {
			p.Missing = append(p.Missing, v.Name)
		} else {
			raw, e := j.Storage.Bytes()
			if e != nil {
				return archiveFreeze{}, e
			}
			retention, e := (worktreetransfer.MissingManager{Git: m.Git, Timeout: m.Timeout}).InspectPresentRetention(ctx, worktreetransfer.MissingSpec{ID: "archive-" + b.Workspace.ID, Checkout: v.Present.Checkout, Common: v.Present.Common, TaskRef: v.Present.TaskRef, Commit: v.Present.Commit, Storage: raw})
			if e != nil {
				return archiveFreeze{}, e
			}
			p.Present = append(p.Present, retention)
		}
	}
	if !root.Absent {
		p.Tree, e = reap.Snapshot(b.Path, nil)
		if e == nil {
			e = reap.FreezePreflight(b.Path, p.Tree)
		}
		if e != nil {
			return archiveFreeze{}, e
		}
	}
	p.Digest, e = p.digest()
	return archiveFreeze{Tree: p.Tree, Digest: p.Digest, missing: p, managed: j.Storage.Managed}, e
}
func (m Manager) readArchiveFreeze(b *model.Bench) (archiveFreeze, error) {
	if !missingSource(b) {
		p, e := reap.ReadFreeze(m.freezeJournal(b.Key), b.Key, b.Path, b.Workspace.Checkpoint.Seal, b.Workspace.Archive.FreezeDigest)
		if e != nil {
			return archiveFreeze{}, e
		}
		return archiveFreeze{Tree: p.Tree, Digest: p.Digest, ordinary: p}, nil
	}
	raw, e := readMissingFile(m.freezeJournal(b.Key))
	if e != nil {
		return archiveFreeze{}, e
	}
	var p MissingFreeze
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e = d.Decode(&p); e != nil {
		return archiveFreeze{}, e
	}
	digest, e := p.digest()
	if e != nil {
		return archiveFreeze{}, e
	}
	if p.Version != missingFreezeVersion || p.Key != b.Key || p.Path != b.Path || p.BenchID != b.Workspace.ID || p.Digest != digest || p.Digest != b.Workspace.Archive.FreezeDigest || !reflect.DeepEqual(p.Checkpoint, b.Workspace.Checkpoint) || !reflect.DeepEqual(p.Transfer, b.Workspace.Transfer) {
		return archiveFreeze{}, errors.New("missing archive intent changed")
	}
	return archiveFreeze{Tree: p.Tree, Digest: p.Digest, missing: &p}, nil
}
func readMissingFile(path string) ([]byte, error) {
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() > reap.MaxManifest {
		return nil, errors.New("invalid missing archive journal")
	}
	raw, e := io.ReadAll(io.LimitReader(f, reap.MaxManifest+1))
	if e != nil {
		return nil, e
	}
	if e = reap.CheckJSON(raw); e != nil {
		return nil, e
	}
	return raw, nil
}

func (m Manager) applyArchiveFreeze(ctx context.Context, b *model.Bench, f archiveFreeze) error {
	if f.missing != nil {
		verifier := worktreetransfer.MissingManager{Git: m.Git, Timeout: m.Timeout, Gate: func(ctx context.Context, _ worktreetransfer.MissingPlan) error { return ctx.Err() }}
		for _, p := range f.missing.Present {
			if e := verifier.VerifyPresentRetention(ctx, p, false); e != nil {
				return e
			}
		}
		for _, p := range f.missing.Present {
			if e := verifier.RetainPresentAdministration(ctx, p); e != nil {
				return e
			}
		}
	}
	return f.apply(b.Path)
}

func (m Manager) completeArchiveRetention(ctx context.Context, f archiveFreeze) error {
	if f.missing != nil {
		for _, p := range f.missing.Present {
			if e := (worktreetransfer.MissingManager{Git: m.Git, Timeout: m.Timeout}).VerifyPresentRetention(ctx, p, true); e != nil {
				return e
			}
		}
	}
	return ctx.Err()
}

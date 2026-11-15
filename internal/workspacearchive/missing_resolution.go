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
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

const RootRemovalReviewVersion = "superbench.missing-root-disposition.v1"
const MaxRootRemovalReview = 16 << 10

// RootRemovalReview is ADDITIONAL explicit authority to remove the CURRENT EMPTY
// fixed source path. The caller deliberately approves that disposition even if
// its provenance is unknown. It does not claim this is the original directory,
// recoverable checkpoint data, or an automatic continuation of the old reap.
// Every attempt is single-use against a present path. Failure after journaling
// requires fresh observation/approval; replay may only confirm current absence.
type RootRemovalReview struct {
	Version, Action, ID, State, DataRoot, Key, Path, BenchID                           string
	TransferID, TransferDigest, FreezeDigest, ReapDigest, RemovalDigest, StorageDigest string
	Root                                                                               hoststorage.RetirementRoot
	ObservedAt                                                                         time.Time
	Digest                                                                             string
}

func rootReviewDigest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func rootReviewSHA(raw []byte) string      { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func (p RootRemovalReview) digest() string { p.Digest = ""; return rootReviewDigest(p) }
func (p RootRemovalReview) Bytes() ([]byte, error) {
	if p.Version != RootRemovalReviewVersion || p.Action != "remove-current-empty-directory" || !keyPattern.MatchString(p.ID) || !keyPattern.MatchString(p.Key) || !keyPattern.MatchString(p.TransferID) || !filepath.IsAbs(p.State) || filepath.Clean(p.State) != p.State || !filepath.IsAbs(p.DataRoot) || filepath.Clean(p.DataRoot) != p.DataRoot || p.Path != filepath.Join(p.DataRoot, "benches", p.Key) || len(p.BenchID) != 32 || p.ObservedAt.IsZero() || p.Root.Absent || p.Root.Device == 0 || p.Root.Inode == 0 || p.Root.ParentInode == 0 || p.Root.Device != p.Root.ParentDevice || os.FileMode(p.Root.Mode)&os.ModeType != os.ModeDir || os.FileMode(p.Root.ParentMode)&os.ModeType != os.ModeDir || p.Digest != p.digest() {
		return nil, errors.New("invalid current-empty-root disposition approval")
	}
	_, offset := p.ObservedAt.Zone()
	if offset != 0 {
		return nil, errors.New("root disposition observation must be UTC")
	}
	for _, s := range []string{p.TransferDigest, p.FreezeDigest, p.ReapDigest, p.RemovalDigest, p.StorageDigest} {
		raw, e := hex.DecodeString(s)
		if e != nil || len(raw) != 32 || hex.EncodeToString(raw) != s {
			return nil, errors.New("invalid root disposition binding")
		}
	}
	raw, e := json.Marshal(p)
	if len(raw) > MaxRootRemovalReview {
		return nil, errors.New("root disposition approval exceeds limit")
	}
	return raw, e
}
func ParseRootRemovalReview(raw []byte) (*RootRemovalReview, error) {
	if len(raw) > MaxRootRemovalReview {
		return nil, errors.New("root disposition approval exceeds limit")
	}
	if e := reap.CheckJSON(raw); e != nil {
		return nil, e
	}
	var p RootRemovalReview
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&p); e != nil {
		return nil, e
	}
	canonical, e := p.Bytes()
	if e != nil {
		return nil, e
	}
	var compact bytes.Buffer
	if e = json.Compact(&compact, raw); e != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return nil, errors.New("root disposition approval must preserve canonical fields")
	}
	return &p, nil
}
func (m Manager) resolutionPath(key, id string) string {
	return filepath.Join(m.State, "aggregate-root-disposition-"+key+"-"+id+".json")
}

// resolutionBinding verifies authority independently of the current leaf. In
// particular verifyMissingArchive intentionally cannot be used: its normal
// retry guard correctly refuses every present root after removal was started.
func (m Manager) resolutionBinding(ctx context.Context, tx *sql.Tx, b *model.Bench, id string) (RootRemovalReview, hoststorage.StorageIdentity, error) {
	var p RootRemovalReview
	var storage hoststorage.StorageIdentity
	if e := m.available(b); e != nil {
		return p, storage, e
	}
	if !missingSource(b) || b.Workspace.Archive == nil || b.Workspace.Archive.FrozenAt == "" || b.Workspace.Archive.ReapingDigest == "" || b.Workspace.Phase != "reaping" && b.Workspace.Phase != "reaped" {
		return p, storage, errors.New("current-root disposition requires a retained missing reap")
	}
	freeze, e := m.readArchiveFreeze(b)
	if e != nil {
		return p, storage, e
	}
	if freeze.missing == nil {
		return p, storage, errors.New("missing archive proof required")
	}
	plan, e := readPlanFile(m.reapJournal(b.Key))
	if e != nil {
		return p, storage, e
	}
	if plan.Version != MissingPlanVersion || plan.Missing == nil || plan.Digest != b.Workspace.Archive.ReapingDigest || !matches(plan, b) || !reflect.DeepEqual(plan.Missing, freeze.missing) {
		return p, storage, errors.New("original missing reap approval changed")
	}
	raw, e := readMissingFile(m.rootRemovalPath(b.Key))
	if e != nil {
		return p, storage, e
	}
	expected, _ := json.Marshal(rootRemoval{Version: "superbench.missing-root-removal.v1", Key: b.Key, Path: b.Path, BenchID: b.Workspace.ID, PlanDigest: plan.Digest})
	if !bytes.Equal(raw, expected) {
		return p, storage, errors.New("original root removal intent changed")
	}
	approved, e := m.missingApproval(tx, b)
	if e != nil {
		return p, storage, e
	}
	storage = approved.Storage
	if e = m.confirm(ctx, b); e != nil {
		return p, storage, e
	}
	for _, member := range approved.Members {
		if member.Missing != nil {
			if e = (worktreetransfer.MissingManager{Git: m.Git, Timeout: m.Timeout}).VerifyQuarantine(ctx, *member.Missing); e != nil {
				return p, storage, e
			}
		}
	}
	for _, retention := range freeze.missing.Present {
		if e := (worktreetransfer.MissingManager{Git: m.Git, Timeout: m.Timeout}).VerifyPresentRetention(ctx, retention, true); e != nil {
			return p, storage, e
		}
	}
	worker := hoststorage.Worker{State: m.State, Data: m.Data}
	if storage.Managed {
		e = worker.VerifyRetirementAuthority(ctx, m.retirement(approved, *plan.Missing))
	} else {
		e = worker.VerifyStorage(ctx, storage)
	}
	if e != nil {
		return p, storage, e
	}

	observedParent, e := hoststorage.CaptureRetirementRoot(storage)
	if e != nil {
		return p, storage, e
	}
	oldParent := plan.Missing.Root
	if observedParent.ParentDevice != oldParent.ParentDevice || observedParent.ParentInode != oldParent.ParentInode || observedParent.ParentMode != oldParent.ParentMode {
		return p, storage, errors.New("original fixed retirement parent changed")
	}
	p = RootRemovalReview{Version: RootRemovalReviewVersion, Action: "remove-current-empty-directory", ID: id, State: m.State, DataRoot: m.Data, Key: b.Key, Path: b.Path, BenchID: b.Workspace.ID, TransferID: b.Workspace.Transfer.ID, TransferDigest: b.Workspace.Transfer.Digest, FreezeDigest: b.Workspace.Archive.FreezeDigest, ReapDigest: plan.Digest, RemovalDigest: rootReviewSHA(raw), StorageDigest: rootReviewDigest(storage)}
	return p, storage, ctx.Err()
}

// observeDispositionRoot requires a physical empty same-filesystem fixed leaf.
// It grants no recursive deletion and never changes permissions or contents.
func observeDispositionRoot(storage hoststorage.StorageIdentity) (hoststorage.RetirementRoot, error) {
	root, e := hoststorage.CaptureRetirementRoot(storage)
	if e != nil {
		return root, e
	}
	if root.Absent {
		return root, nil
	}
	path := storage.Assignment.Path
	physical, e := filepath.EvalSymlinks(path)
	if e != nil || physical != path {
		return root, errors.New("current disposition root is not physical")
	}
	directory, e := os.OpenRoot(path)
	if e != nil {
		return root, e
	}
	defer directory.Close()
	f, e := directory.Open(".")
	if e != nil {
		return root, e
	}
	defer f.Close()
	entries, e := f.ReadDir(1)
	if len(entries) != 0 || e != io.EOF {
		return root, errors.New("current-root disposition requires an empty directory")
	}
	after, e := hoststorage.CaptureRetirementRoot(storage)
	if e != nil {
		return root, e
	}
	if after != root {
		return root, errors.New("current disposition root changed during observation")
	}
	return root, nil
}

// InspectRootRemoval never mutates a journal, file, quota, history or state row.
// A fresh review ID is required even when the still-present directory might be
// the original one: inode/mode reuse cannot establish that after a crash.
func (m Manager) InspectRootRemoval(ctx context.Context, key, id string) (*RootRemovalReview, error) {
	ctx, cancel, e := m.bounded(ctx, key)
	if e != nil {
		return nil, e
	}
	defer cancel()
	if !keyPattern.MatchString(id) {
		return nil, errors.New("fresh root disposition ID required")
	}
	var out *RootRemovalReview
	e = m.Within(func(update workspace.Update) error {
		return update(func(rows benchstate.Database, tx *sql.Tx) error {
			if _, e := os.Lstat(m.resolutionPath(key, id)); !os.IsNotExist(e) {
				return errors.New("root disposition ID is already used")
			}
			p, storage, e := m.resolutionBinding(ctx, tx, rows[key], id)
			if e != nil {
				return e
			}
			p.Root, e = observeDispositionRoot(storage)
			if e != nil {
				return e
			}
			if p.Root.Absent {
				return errors.New("root is absent; resume the original reap without additional disposition")
			}
			p.ObservedAt = time.Now().UTC()
			p.Digest = p.digest()
			if _, e = p.Bytes(); e != nil {
				return e
			}
			out = &p
			return ctx.Err()
		})
	})
	return out, e
}

// ReconcileRootRemoval applies ONLY the separately reviewed current empty path
// disposition. It never retires quota, releases history, marks reaped or changes
// the original approval. Ordinary Reap must subsequently finish those duties.
func (m Manager) ReconcileRootRemoval(ctx context.Context, p RootRemovalReview) error {
	raw, e := p.Bytes()
	if e != nil {
		return e
	}
	ctx, cancel, e := m.bounded(ctx, p.Key)
	if e != nil {
		return e
	}
	defer cancel()
	if m.SaveIntent == nil || p.State != m.State || p.DataRoot != m.Data || p.ObservedAt.After(time.Now().UTC()) {
		return errors.New("root disposition requires current storage and durable intent")
	}
	return m.Within(func(update workspace.Update) error {
		return update(func(rows benchstate.Database, tx *sql.Tx) error {
			current, storage, e := m.resolutionBinding(ctx, tx, rows[p.Key], p.ID)
			if e != nil {
				return e
			}
			current.Root, current.ObservedAt, current.Digest = p.Root, p.ObservedAt, p.Digest
			if current != p {
				return errors.New("root disposition authority changed after review")
			}
			path := m.resolutionPath(p.Key, p.ID)
			prior, e := readMissingFile(path)
			if e == nil {
				if !bytes.Equal(prior, raw) {
					return errors.New("root disposition ID is bound to another approval")
				}
				observed, e := observeDispositionRoot(storage)
				if e != nil {
					return e
				}
				if !observed.Absent {
					return errors.New("root disposition already started; present path requires fresh review")
				}
				if observed.ParentDevice != p.Root.ParentDevice || observed.ParentInode != p.Root.ParentInode || observed.ParentMode != p.Root.ParentMode {
					return errors.New("root disposition parent changed")
				}
				return ctx.Err()
			}
			if !os.IsNotExist(e) {
				return e
			}
			observed, e := observeDispositionRoot(storage)
			if e != nil {
				return e
			}
			if observed != p.Root {
				return errors.New("current empty root changed after disposition review")
			}
			if e = m.SaveIntent(path, raw); e != nil {
				return e
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			// Externally provided persistence/Git callbacks may have changed the path.
			// Repeat authority and exact emptiness after journaling, before any unlink.
			again, _, e := m.resolutionBinding(ctx, tx, rows[p.Key], p.ID)
			if e != nil {
				return e
			}
			again.Root, again.ObservedAt, again.Digest = p.Root, p.ObservedAt, p.Digest
			if again != p {
				return errors.New("root disposition authority changed before unlink")
			}
			observed, e = observeDispositionRoot(storage)
			if e != nil {
				return e
			}
			if observed != p.Root {
				return errors.New("current empty root changed before disposition")
			}
			parent, e := os.OpenRoot(filepath.Dir(p.Path))
			if e != nil {
				return e
			}
			defer parent.Close()
			if e = parent.Remove(p.Key); e != nil {
				return e
			}
			f, e := parent.Open(".")
			if e != nil {
				return e
			}
			defer f.Close()
			if e = f.Sync(); e != nil {
				return e
			}
			observed, e = observeDispositionRoot(storage)
			if e != nil {
				return e
			}
			if !observed.Absent {
				return errors.New("root returned after disposition; preserve and review again")
			}
			return ctx.Err()
		})
	})
}

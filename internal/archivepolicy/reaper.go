package archivepolicy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

// Reaper journals reviewed deletion authority before removing unchanged entries.
// Update must commit each callback under an exclusive state transaction; Read
// must hold its read lock throughout inspection. Neither callback may substitute
// a different record for the requested key.
type Reaper struct {
	Update, Read           func(string, func(*model.Bench) error) error
	Inspector              Inspector
	Journal, FreezeJournal func(string) string
	SaveIntent             func(string, []byte) error
	Git                    func(string, ...string) (string, error)
	ReleaseQuota           func(*model.Bench) error
	AfterDelete            func() error
	Busy                   error
	ValidKey               func(string) bool
}

func (r Reaper) makePlan(b *model.Bench) (*reap.Plan, error) {
	if e := r.Inspector.Paths(b); e != nil {
		return nil, e
	}
	if b.Lease != nil {
		return nil, r.Busy
	}
	if b.ArchivedAt == "" || b.ReapedAt != "" || b.ReapingDigest != "" {
		return nil, errors.New("bench must be archived and not already reaping")
	}
	if e := r.Inspector.Checkout(b); e != nil {
		return nil, e
	}
	if b.ArchiveFreezeDigest == "" || b.ArchiveFrozenAt == "" {
		return nil, errors.New("repeat archive to finish the read-only transition before planning deletion")
	}
	freeze, e := reap.ReadFreeze(r.FreezeJournal(b.Key), b.Key, b.Path, b.Checkpoint, b.ArchiveFreezeDigest)
	if e != nil {
		return nil, e
	}
	if _, e = reap.Frozen(b.Path, freeze.Tree); e != nil {
		return nil, e
	}
	return r.Inspector.Plan(b)
}

func (r Reaper) Plan(key string) (*reap.Plan, error) {
	var p *reap.Plan
	err := r.Read(key, func(b *model.Bench) error {
		var err error
		p, err = r.makePlan(b)
		return err
	})
	return p, err
}

// Reap requires the concrete reviewed manifest. It journals approval before any
// deletion. Retry accepts only unchanged remaining entries, never new files.
func (r Reaper) Reap(p reap.Plan) (*model.Bench, error) {
	digest, e := p.DigestValue()
	if e != nil {
		return nil, e
	}
	if !r.ValidKey(p.Key) || (p.Version != "superbench.reap.v1" && p.Version != "superbench.reap.v2" && p.Version != "superbench.reap.v3") || p.Digest != digest {
		return nil, errors.New("invalid approved reap plan")
	}
	e = r.Update(p.Key, func(b *model.Bench) error {
		var e error
		if e = r.Inspector.Paths(b); e != nil {
			return e
		}
		if b.Path != p.Path || b.Checkpoint != p.Checkpoint || b.ArchivedAt == "" || b.Lease != nil {
			return errors.New("reap plan conflicts with bench state")
		}
		if p.Version != "superbench.reap.v1" && p.ReapAfter != b.ReapAfter {
			return errors.New("reap plan grace deadline conflicts with bench state")
		}
		if b.ReapingDigest != "" {
			if b.ReapingDigest != p.Digest {
				return errors.New("another reap intent is retained")
			}
			return nil
		}
		if p.Version != "superbench.reap.v3" {
			return errors.New("legacy reap approval requires a fresh review")
		}
		if e = Eligible(b.ArchivedAt, b.ReapAfter, time.Now()); e != nil {
			return e
		}
		if p.FreezeDigest != b.ArchiveFreezeDigest || b.ArchiveFrozenAt == "" {
			return errors.New("reap plan has no completed read-only archive")
		}
		current, e := r.makePlan(b)
		if e != nil {
			return e
		}
		if current.Digest != p.Digest {
			return errors.New("bench changed after plan; review a fresh plan")
		}
		raw, e := json.Marshal(p)
		if e != nil {
			return e
		}
		if e = r.SaveIntent(r.Journal(p.Key), raw); e != nil {
			return e
		}
		b.ReapingDigest = p.Digest
		return nil
	})
	if e != nil {
		return nil, e
	}
	var out *model.Bench
	e = r.Update(p.Key, func(b *model.Bench) error {
		var e error
		if b.ReapingDigest != p.Digest || b.Lease != nil {
			return errors.New("reap intent changed")
		}
		if b.ReapedAt != "" {
			copy := *b
			out = &copy
			return nil
		}
		raw, e := os.ReadFile(r.Journal(p.Key))
		if e != nil {
			return e
		}
		var saved reap.Plan
		if e = json.Unmarshal(raw, &saved); e != nil {
			return e
		}
		savedDigest, e := saved.DigestValue()
		if e != nil {
			return e
		}
		if saved.Digest != p.Digest || savedDigest != p.Digest {
			return errors.New("retained reap journal changed")
		}
		if e = r.Inspector.Remote(b); e != nil {
			return e
		}
		if _, e = os.Lstat(b.Path); e == nil {
			deleteContents := reap.DeleteContents
			if saved.Version == "superbench.reap.v3" {
				deleteContents = reap.DeleteFrozenContents
			}
			if e = deleteContents(b.Path, saved.Tree); e != nil {
				return e
			}
		} else if !os.IsNotExist(e) {
			return e
		}
		if r.AfterDelete != nil {
			if e = r.AfterDelete(); e != nil {
				return e
			}
		}
		if b.ProjectID != 0 {
			if e = r.ReleaseQuota(b); e != nil {
				return e
			}
		} else if e = os.Remove(b.Path); e != nil && !os.IsNotExist(e) {
			return e
		}
		// Remove only this registration. Never prune other benches or delete refs.
		list, e := r.Git(b.Mirror, "worktree", "list", "--porcelain", "-z")
		if e != nil {
			return e
		}
		parent, e := filepath.EvalSymlinks(filepath.Dir(b.Path))
		if e != nil {
			return e
		}
		canonicalTarget := filepath.Join(parent, filepath.Base(b.Path))
		for _, field := range strings.Split(list, "\x00") {
			if !strings.HasPrefix(field, "worktree ") {
				continue
			}
			path := strings.TrimPrefix(field, "worktree ")
			if path == b.Path || path == canonicalTarget {
				if _, e = r.Git(b.Mirror, "worktree", "remove", "--force", path); e != nil {
					return e
				}
				break
			}
		}
		b.ReapedAt = time.Now().UTC().Format(time.RFC3339Nano)
		copy := *b
		out = &copy
		return nil
	})
	return out, e
}

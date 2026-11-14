package bench

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

type ReapPlan = reap.Plan

func (s Store) archiveInspector() archivepolicy.Inspector {
	return archivepolicy.Inspector{Data: s.Data, Git: git, MirrorPath: s.mirrorPath, Clean: clean, Confirmed: confirmed, CheckpointError: ErrCheckpoint, ValidKey: keyPattern.MatchString}
}
func archiveRemote(b *Bench) error {
	return archivepolicy.Inspector{Git: git, Confirmed: confirmed}.Remote(b)
}
func archiveCheckout(b *Bench) error {
	return archivepolicy.Inspector{Git: git, Clean: clean, Confirmed: confirmed, CheckpointError: ErrCheckpoint}.Checkout(b)
}
func archivePaths(s Store, b *Bench) error { return s.archiveInspector().Paths(b) }
func (s Store) Archive(key string) (*Bench, error) {
	return s.ArchiveWithGrace(key, archivepolicy.DefaultGrace)
}
func (s Store) ArchiveWithGrace(key string, grace time.Duration) (*Bench, error) {
	if e := archivepolicy.Duration(grace); e != nil {
		return nil, e
	}
	var out *Bench
	e := s.transaction(func(db database) error {
		b, e := get(db, key)
		if e != nil {
			return e
		}
		if e = archivePaths(s, b); e != nil {
			return e
		}
		if b.Lease != nil {
			return ErrBusy
		}
		if b.ArchiveFreezeDigest == "" && b.ReapingDigest == "" && b.ReapedAt == "" {
			if e = archiveCheckout(b); e != nil {
				return e
			}
			plan, e := reap.NewFreeze(b.Key, b.Path, b.Checkpoint)
			if e != nil {
				return e
			}
			raw, e := plan.Bytes()
			if e != nil {
				return e
			}
			if e = s.saveLifecycleIntent(s.freezeJournal(b.Key), raw); e != nil {
				return e
			}
			b.ArchiveFreezeDigest = plan.Digest
		}
		if b.ArchivedAt == "" {
			b.ArchivedAt = time.Now().UTC().Format(time.RFC3339Nano)
			b.ReapAfter, e = archivepolicy.Deadline(b.ArchivedAt, grace)
			if e != nil {
				return e
			}
		}
		copy := *b
		out = &copy
		return nil
	})
	if e != nil || out.ReapingDigest != "" || out.ReapedAt != "" {
		return out, e
	}
	e = s.transaction(func(db database) error {
		b, e := get(db, key)
		if e != nil {
			return e
		}
		if b.ArchiveFreezeDigest != out.ArchiveFreezeDigest || b.Lease != nil || b.ReapingDigest != "" {
			return errors.New("archive permission intent changed")
		}
		plan, e := reap.ReadFreeze(s.freezeJournal(key), b.Key, b.Path, b.Checkpoint, b.ArchiveFreezeDigest)
		if e != nil {
			return e
		}
		if e = reap.Freeze(b.Path, plan.Tree); e != nil {
			return e
		}
		if e = archiveCheckout(b); e != nil {
			return e
		}
		if b.ArchiveFrozenAt == "" {
			b.ArchiveFrozenAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		copy := *b
		out = &copy
		return nil
	})
	return out, e
}
func (s Store) makeReapPlan(b *Bench) (*ReapPlan, error) {
	if e := archivePaths(s, b); e != nil {
		return nil, e
	}
	if b.Lease != nil {
		return nil, ErrBusy
	}
	if b.ArchivedAt == "" || b.ReapedAt != "" || b.ReapingDigest != "" {
		return nil, errors.New("bench must be archived and not already reaping")
	}
	if e := archiveCheckout(b); e != nil {
		return nil, e
	}
	if b.ArchiveFreezeDigest == "" || b.ArchiveFrozenAt == "" {
		return nil, errors.New("repeat archive to finish the read-only transition before planning deletion")
	}
	freeze, e := reap.ReadFreeze(s.freezeJournal(b.Key), b.Key, b.Path, b.Checkpoint, b.ArchiveFreezeDigest)
	if e != nil {
		return nil, e
	}
	if _, e = reap.Frozen(b.Path, freeze.Tree); e != nil {
		return nil, e
	}
	return s.archiveInspector().Plan(b)
}

func (s Store) PlanReap(key string) (*ReapPlan, error) {
	var p *ReapPlan
	e := s.read(func(db database) error {
		b, e := get(db, key)
		if e != nil {
			return e
		}
		p, e = s.makeReapPlan(b)
		return e
	})
	return p, e
}
func (s Store) reapJournal(key string) string { return filepath.Join(s.State, "reap-"+key+".json") }

// Reap requires the concrete reviewed manifest. It journals approval before any
// deletion. Retry accepts only unchanged remaining entries, never new files.
func (s Store) Reap(p ReapPlan) (*Bench, error) {
	digest, e := p.DigestValue()
	if e != nil {
		return nil, e
	}
	if !keyPattern.MatchString(p.Key) || (p.Version != "superbench.reap.v1" && p.Version != "superbench.reap.v2" && p.Version != "superbench.reap.v3") || p.Digest != digest {
		return nil, errors.New("invalid approved reap plan")
	}
	e = s.transaction(func(db database) error {
		b, e := get(db, p.Key)
		if e != nil {
			return e
		}
		if e = archivePaths(s, b); e != nil {
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
		if e = archivepolicy.Eligible(b.ArchivedAt, b.ReapAfter, time.Now()); e != nil {
			return e
		}
		if p.FreezeDigest != b.ArchiveFreezeDigest || b.ArchiveFrozenAt == "" {
			return errors.New("reap plan has no completed read-only archive")
		}
		current, e := s.makeReapPlan(b)
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
		if e = s.saveLifecycleIntent(s.reapJournal(p.Key), raw); e != nil {
			return e
		}
		b.ReapingDigest = p.Digest
		return nil
	})
	if e != nil {
		return nil, e
	}
	var out *Bench
	e = s.transaction(func(db database) error {
		b, e := get(db, p.Key)
		if e != nil {
			return e
		}
		if b.ReapingDigest != p.Digest || b.Lease != nil {
			return errors.New("reap intent changed")
		}
		if b.ReapedAt != "" {
			copy := *b
			out = &copy
			return nil
		}
		raw, e := os.ReadFile(s.reapJournal(p.Key))
		if e != nil {
			return e
		}
		var saved ReapPlan
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
		if e = archiveRemote(b); e != nil {
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
		if s.reapAfterDelete != nil {
			if e = s.reapAfterDelete(); e != nil {
				return e
			}
		}
		if b.ProjectID != 0 {
			if e = s.releaseQuota(b); e != nil {
				return e
			}
		} else if e = os.Remove(b.Path); e != nil && !os.IsNotExist(e) {
			return e
		}
		// Remove only this registration. Never prune other benches or delete refs.
		list, e := git(b.Mirror, "worktree", "list", "--porcelain", "-z")
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
				if _, e = git(b.Mirror, "worktree", "remove", "--force", path); e != nil {
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

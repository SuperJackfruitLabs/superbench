package bench

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

type ReapPlan = reap.Plan

func archivePaths(s Store, b *Bench) error {
	if !keyPattern.MatchString(b.Key) || b.Path != filepath.Join(s.Data, "benches", b.Key) || b.Mirror != s.mirrorPath(b.Repo) {
		return errors.New("bench paths conflict with store")
	}
	actual, e := filepath.EvalSymlinks(b.Path)
	if e == nil {
		data, e := filepath.EvalSymlinks(s.Data)
		if e != nil {
			return e
		}
		if actual != filepath.Join(data, "benches", b.Key) {
			return errors.New("bench path escapes its data directory")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	return nil
}
func archiveRemote(b *Bench) error {
	origin, e := git(b.Mirror, "remote", "get-url", "origin")
	if e != nil {
		return e
	}
	if origin != b.Repo {
		return errors.New("mirror origin changed")
	}
	copy := *b
	copy.Path = b.Mirror
	return confirmed(&copy, b.Checkpoint)
}
func archiveCheckout(b *Bench) error {
	if e := clean(b); e != nil {
		return e
	}
	sha, e := head(b)
	if e != nil {
		return e
	}
	if b.Checkpoint == "" || sha != b.Checkpoint {
		return ErrCheckpoint
	}
	branch, e := git(b.Path, "symbolic-ref", "--quiet", "HEAD")
	if e != nil {
		return e
	}
	if branch != "refs/heads/"+b.Branch {
		return ErrCheckpoint
	}
	common, e := git(b.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return e
	}
	actual, e := filepath.EvalSymlinks(common)
	if e != nil {
		return e
	}
	expected, e := filepath.EvalSymlinks(b.Mirror)
	if e != nil {
		return e
	}
	if actual != expected {
		return errors.New("worktree belongs to another mirror")
	}
	admin, e := git(b.Path, "rev-parse", "--absolute-git-dir")
	if e != nil {
		return e
	}
	if _, e = os.Lstat(filepath.Join(admin, "locked")); e == nil {
		return errors.New("worktree is locked; review its lock before archive")
	} else if !os.IsNotExist(e) {
		return e
	}
	return archiveRemote(b)
}
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
		if b.ArchivedAt == "" {
			if e = archiveCheckout(b); e != nil {
				return e
			}
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
	// Compare raw file/link content with HEAD objects, rather than assuming an
	// index flag or a checkout filter proves that these local bytes recover.
	rawFiles, e := exec.Command("git", "-C", b.Path, "ls-tree", "-r", "-z", "HEAD").Output()
	if e != nil {
		return nil, e
	}
	objects := map[string]string{}
	for _, row := range strings.Split(string(rawFiles), "\x00") {
		header, path, ok := strings.Cut(row, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(header)
		if len(fields) == 3 && fields[1] == "blob" {
			objects[path] = fields[2]
		}
	}
	tree, e := reap.Snapshot(b.Path, nil)
	if e != nil {
		return nil, e
	}
	for i := range tree.Entries {
		v := &tree.Entries[i]
		oid, ok := objects[v.Path]
		v.Recoverable = ok && (oid == v.GitSHA1 || oid == v.GitSHA256)
	}
	p := &ReapPlan{Version: "superbench.reap.v2", ReapAfter: b.ReapAfter, Key: b.Key, Path: b.Path, Checkpoint: b.Checkpoint, Tree: tree}
	p.Digest, e = p.DigestValue()
	return p, e
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
	if !keyPattern.MatchString(p.Key) || (p.Version != "superbench.reap.v1" && p.Version != "superbench.reap.v2") || p.Digest != digest {
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
		if p.Version == "superbench.reap.v2" && p.ReapAfter != b.ReapAfter {
			return errors.New("reap plan grace deadline conflicts with bench state")
		}
		if b.ReapingDigest != "" {
			if b.ReapingDigest != p.Digest {
				return errors.New("another reap intent is retained")
			}
			return nil
		}
		if p.Version != "superbench.reap.v2" {
			return errors.New("legacy reap approval requires a fresh review")
		}
		if e = archivepolicy.Eligible(b.ArchivedAt, b.ReapAfter, time.Now()); e != nil {
			return e
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
		if len(raw) > reap.MaxManifest {
			return errors.New("reap plan exceeds limit")
		}
		space, e := s.space(s.State)
		if e != nil {
			return e
		}
		if space.Free < uint64(len(raw))*2+(1<<20) {
			return errors.New("insufficient state space for durable reap intent")
		}
		if e = atomicWrite(s.State, s.reapJournal(p.Key), raw); e != nil {
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
			if e = reap.DeleteContents(b.Path, saved.Tree); e != nil {
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

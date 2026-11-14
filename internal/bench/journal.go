package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Creation is an immutable intent retained after registration. It pins the
// source commit before worktree mutation so a retry never silently changes base.
type creation struct {
	Bench      Bench
	Source     string
	Base       string
	Recovering bool
}

func (s Store) journalPath(key string) string { return filepath.Join(s.State, "create-"+key+".json") }
func (s Store) pending(key string, o Options, recovering bool) (*creation, error) {
	raw, err := os.ReadFile(s.journalPath(key))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c creation
	if err = json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("invalid creation journal: %w", err)
	}
	if c.Bench.Key != key || c.Bench.Repo != o.Repo || c.Bench.Branch != o.Branch || c.Base != o.Base || c.Recovering != recovering || c.Source == "" || c.Bench.Lease != nil || c.Bench.Path != filepath.Join(s.Data, "benches", key) || c.Bench.Mirror != s.mirrorPath(o.Repo) {
		return nil, errors.New("creation journal conflicts with request; preserve files and reconcile manually")
	}
	return &c, nil
}
func (s Store) record(c creation) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.State, s.journalPath(c.Bench.Key), raw)
}
func finishCreate(c *creation) (*Bench, error) {
	b := c.Bench
	if stat, err := os.Lstat(b.Path); err == nil {
		if stat.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("worktree target is a symlink")
		}
		entries, e := os.ReadDir(b.Path)
		if e != nil {
			return nil, e
		}
		if len(entries) == 0 && b.ProjectID != 0 {
			return materializeWorktree(c)
		}
		// Only adopt the exact journaled checkout. No reset, checkout or cleanup.
		top, e := git(b.Path, "rev-parse", "--show-toplevel")
		if e != nil {
			return nil, e
		}
		branch, e := git(b.Path, "symbolic-ref", "--quiet", "HEAD")
		if e != nil {
			return nil, e
		}
		common, e := git(b.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if e != nil {
			return nil, e
		}
		same := func(a, b string) bool {
			x, e := filepath.EvalSymlinks(a)
			y, f := filepath.EvalSymlinks(b)
			return e == nil && f == nil && x == y
		}
		sha, e := head(&b)
		if e != nil {
			return nil, e
		}
		if !same(top, b.Path) || !same(common, b.Mirror) || branch != "refs/heads/"+b.Branch || sha != c.Source {
			return nil, errors.New("journaled checkout changed; refusing automatic adoption")
		}
		if err = clean(&b); err != nil {
			return nil, err
		}
		return &b, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return materializeWorktree(c)
}
func materializeWorktree(c *creation) (*Bench, error) {
	b := c.Bench
	if err := os.MkdirAll(filepath.Dir(b.Path), 0700); err != nil {
		return nil, err
	}
	if local, e := git(b.Mirror, "rev-parse", "--verify", "refs/heads/"+b.Branch); e == nil {
		if local != c.Source {
			return nil, errors.New("local task branch differs from journal; refusing reset")
		}
		if _, e = git(b.Mirror, "worktree", "add", b.Path, b.Branch); e != nil {
			return nil, e
		}
	} else if _, e = git(b.Mirror, "worktree", "add", "-b", b.Branch, b.Path, c.Source); e != nil {
		return nil, e
	}
	return &b, nil
}

func (s Store) mirrorPath(repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(s.Data, "mirrors", hex.EncodeToString(sum[:])+".git")
}

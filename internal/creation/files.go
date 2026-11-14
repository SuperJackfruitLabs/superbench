package creation

import (
	"encoding/json"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"os"
	"path/filepath"
)

type Files struct {
	State, Data string
	Git         func(string, ...string) (string, error)
	Write       func(string, string, []byte) error
}
type Intent struct {
	Bench              model.Bench
	Source             string
	Base               string
	Recovering         bool
	QuotaRequestPinned bool `json:",omitempty"`
}

func (s Files) JournalPath(key string) string { return filepath.Join(s.State, "create-"+key+".json") }
func (s Files) Pending(key string, o model.Options, recovering bool) (*Intent, error) {
	raw, err := os.ReadFile(s.JournalPath(key))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Intent
	if err = json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("invalid creation journal: %w", err)
	}
	if c.Bench.Key != key || c.Bench.Repo != o.Repo || c.Bench.Branch != o.Branch || c.Base != o.Base || c.Recovering != recovering || c.Source == "" || c.Bench.Lease != nil || c.Bench.Path != filepath.Join(s.Data, "benches", key) || c.Bench.Mirror != repository.Path(s.Data, o.Repo) {
		return nil, errors.New("creation journal conflicts with request; preserve files and reconcile manually")
	}
	return &c, nil
}
func (s Files) Record(c Intent) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return s.Write(s.State, s.JournalPath(c.Bench.Key), raw)
}
func (s Files) Finish(c *Intent) (*model.Bench, error) {
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
			return s.materializeWorktree(c)
		}
		// Only adopt the exact journaled checkout. No reset, checkout or cleanup.
		top, e := s.Git(b.Path, "rev-parse", "--show-toplevel")
		if e != nil {
			return nil, e
		}
		branch, e := s.Git(b.Path, "symbolic-ref", "--quiet", "HEAD")
		if e != nil {
			return nil, e
		}
		common, e := s.Git(b.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if e != nil {
			return nil, e
		}
		same := func(a, b string) bool {
			x, e := filepath.EvalSymlinks(a)
			y, f := filepath.EvalSymlinks(b)
			return e == nil && f == nil && x == y
		}
		sha, e := s.Git(b.Path, "rev-parse", "HEAD")
		if e != nil {
			return nil, e
		}
		if !same(top, b.Path) || !same(common, b.Mirror) || branch != "refs/heads/"+b.Branch || sha != c.Source {
			return nil, errors.New("journaled checkout changed; refusing automatic adoption")
		}
		if out, e := s.Git(b.Path, "status", "--porcelain", "--untracked-files=all"); e != nil {
			return nil, e
		} else if out != "" {
			return nil, model.ErrDirty
		}
		return &b, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s.materializeWorktree(c)
}
func (s Files) materializeWorktree(c *Intent) (*model.Bench, error) {
	b := c.Bench
	if err := os.MkdirAll(filepath.Dir(b.Path), 0700); err != nil {
		return nil, err
	}
	if local, e := s.Git(b.Mirror, "rev-parse", "--verify", "refs/heads/"+b.Branch); e == nil {
		if local != c.Source {
			return nil, errors.New("local task branch differs from journal; refusing reset")
		}
		if _, e = s.Git(b.Mirror, "worktree", "add", b.Path, b.Branch); e != nil {
			return nil, e
		}
	} else if _, e = s.Git(b.Mirror, "worktree", "add", "-b", b.Branch, b.Path, c.Source); e != nil {
		return nil, e
	}
	return &b, nil
}

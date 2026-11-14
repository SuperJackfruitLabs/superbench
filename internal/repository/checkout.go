package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Checkout struct{ Path, Branch, Mirror string }

func (m Mirrors) RefreshCheckout(ctx context.Context, repo string, c Checkout) error {
	ctx, cancel, e := m.deadline(ctx)
	if e != nil {
		return e
	}
	defer cancel()
	if e = m.location(repo, c.Mirror); e != nil {
		return e
	}
	info, e := os.Lstat(c.Path)
	if e != nil {
		return e
	}
	if !info.IsDir() {
		return errors.New("registered checkout must be a physical directory")
	}
	common, e := git(ctx, c.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return e
	}
	actual, e := filepath.EvalSymlinks(strings.TrimSuffix(common, "\n"))
	if e != nil {
		return e
	}
	expected, e := filepath.EvalSymlinks(c.Mirror)
	if e != nil {
		return e
	}
	if actual != expected {
		return errors.New("checkout belongs to another mirror; repair its registration explicitly")
	}
	branch, e := git(ctx, c.Path, "symbolic-ref", "--quiet", "HEAD")
	if e != nil {
		return e
	}
	if strings.TrimSuffix(branch, "\n") != "refs/heads/"+c.Branch {
		return errors.New("checkout no longer names its registered task branch")
	}
	return m.refresh(ctx, repo, c.Mirror)
}

// Package memberinspect validates a registered workspace's checkout vector
// without refreshing sources, moving refs or changing files. A legacy checkout
// is read directly. A Fresh payload-private tree is observed only through its
// freshtree route; the daemon reads nothing inside it, only its own mirrors.
package memberinspect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacematerialize"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// Inspector holds the daemon's own trimmed Git, used for legacy checkouts and
// every daemon mirror, and the Fresh route when the workspace selected one.
type Inspector struct {
	Data string
	Git  func(context.Context, string, ...string) (string, error)
	Tree freshtree.Tree
	// ImportMemoryKiB bounds each checkpoint import Git process; zero is the default.
	ImportMemoryKiB uint64
}

func samePhysical(a, b string) bool {
	x, e := filepath.EvalSymlinks(a)
	y, f := filepath.EvalSymlinks(b)
	return e == nil && f == nil && x == y
}

// Root validates storage ancestry and, when clean, that the root holds only
// registered entries. The Fresh root's contents are observed by the helper.
func (i Inspector) Root(ctx context.Context, b *model.Bench, clean bool) error {
	if err := workspacepolicy.Validate(b); err != nil {
		return err
	}
	if b.Path != filepath.Join(i.Data, "benches", b.Key) {
		return errors.New("workspace root differs from configured data")
	}
	for _, path := range []string{i.Data, filepath.Join(i.Data, "benches"), b.Path, filepath.Join(i.Data, "mirrors")} {
		if err := workspacematerialize.PhysicalDirectory(path, false); err != nil {
			return err
		}
	}
	if i.Tree != nil {
		return i.Tree.Root(ctx, b, clean)
	}
	if clean {
		names := map[string]bool{}
		for _, member := range b.Workspace.Members {
			names[member.Name] = true
		}
		entries, err := os.ReadDir(b.Path)
		if err != nil {
			return err
		}
		var stray []string
		for _, entry := range entries {
			if !names[entry.Name()] || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				stray = append(stray, entry.Name())
			}
		}
		if len(stray) != 0 {
			return model.Dirty(stray)
		}
	}
	return nil
}

// Member validates one checkout's identity against its registration. A legacy
// checkout shares its daemon mirror's object store; a Fresh checkout has its
// registered independent admin and is read only as the payload account.
func (i Inspector) Member(ctx context.Context, b *model.Bench, member model.WorkspaceMember, clean bool) (model.WorkspaceCommit, error) {
	paths := []string{member.Path, member.Mirror}
	checkout := func(args ...string) (string, error) { return i.git(ctx, member.Path, args...) }
	same := func(top, common string) bool {
		return samePhysical(top, member.Path) && samePhysical(common, member.Mirror)
	}
	if i.Tree != nil {
		paths = []string{member.Mirror}
		checkout = func(args ...string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			out, err := i.Tree.Git(ctx, b, member.Name, args...)
			return strings.TrimSpace(out), err
		}
		same = func(top, common string) bool { return top == member.Path && common == freshtree.Admin(b, member.Name) }
	}
	for _, path := range paths {
		if err := workspacematerialize.PhysicalDirectory(path, false); err != nil {
			return model.WorkspaceCommit{}, err
		}
	}
	top, err := checkout("rev-parse", "--show-toplevel")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	common, err := checkout("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	branch, err := checkout("symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	if !same(top, common) || branch != "refs/heads/"+member.Branch {
		return model.WorkspaceCommit{}, errors.New("workspace checkout identity changed; preserve and reconcile")
	}
	bare, err := i.git(ctx, member.Mirror, "rev-parse", "--is-bare-repository")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	origin, err := i.git(ctx, member.Mirror, "config", "--null", "--get-all", "remote.origin.url")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	checkoutOrigin, err := checkout("config", "--null", "--get-all", "remote.origin.url")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	format, err := checkout("rev-parse", "--show-object-format=storage")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	if bare != "true" || origin != member.Repository+"\x00" || checkoutOrigin != origin || format != member.ObjectFormat {
		return model.WorkspaceCommit{}, errors.New("workspace mirror identity or object format changed")
	}
	if clean {
		status, err := checkout("status", "--porcelain", "--untracked-files=all")
		if err != nil {
			return model.WorkspaceCommit{}, err
		}
		if status != "" {
			return model.WorkspaceCommit{}, model.DirtyMember(b, member.Name, status)
		}
	}
	head, err := checkout("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	if !model.ValidCommit(head) || head != strings.ToLower(head) || member.ObjectFormat == "sha1" && len(head) != 40 || member.ObjectFormat == "sha256" && len(head) != 64 {
		return model.WorkspaceCommit{}, errors.New("workspace HEAD has an invalid object identity")
	}
	return model.WorkspaceCommit{Name: member.Name, Commit: head}, nil
}

func (i Inspector) git(ctx context.Context, dir string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	out, err := i.Git(ctx, dir, args...)
	return strings.TrimSpace(out), err
}

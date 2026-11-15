package workspacearchive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// legacyRegistrations preserves already-retained ordinary pre-transfer approvals.
// It never applies to a transferred source. It never prunes. During deletion retry a missing registration is
// permitted, but an existing target must still have its exact branch identity.
func (m Manager) legacyRegistrations(ctx context.Context, b *model.Bench, missing, remove bool) error {
	data, err := filepath.EvalSymlinks(m.Data)
	if err != nil {
		return err
	}
	for _, member := range b.Workspace.Members {
		out, err := m.Git(ctx, member.Mirror, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			return err
		}
		canonical := filepath.Join(data, "benches", b.Key, member.Name)
		var selected string
		for _, record := range strings.Split(out, "\x00\x00") {
			var path, branch string
			locked := false
			for _, field := range strings.Split(record, "\x00") {
				if strings.HasPrefix(field, "worktree ") {
					path = strings.TrimPrefix(field, "worktree ")
				}
				if strings.HasPrefix(field, "branch ") {
					branch = strings.TrimPrefix(field, "branch ")
				}
				if field == "locked" || strings.HasPrefix(field, "locked ") {
					locked = true
				}
			}
			target := path == member.Path || path == canonical
			if branch == "refs/heads/"+member.Branch && !target {
				return errors.New("aggregate task branch registered to another worktree")
			}
			if !target {
				continue
			}
			if selected != "" || branch != "refs/heads/"+member.Branch || locked {
				return errors.New("aggregate worktree registration changed or is locked")
			}
			selected = path
		}
		if selected == "" {
			if missing {
				continue
			}
			return errors.New("aggregate member worktree registration is missing")
		}
		if !remove {
			// Git's admin lock is outside the checkout tree and must be independently
			// checked before publishing archive or deletion authority.
			if !missing {
				admin, err := m.Git(ctx, member.Path, "rev-parse", "--absolute-git-dir")
				if err != nil {
					return err
				}
				if _, err := os.Lstat(filepath.Join(strings.TrimSpace(admin), "locked")); err == nil {
					return errors.New("aggregate worktree is locked")
				} else if !os.IsNotExist(err) {
					return err
				}
			}
			continue
		}
		if _, err := os.Lstat(member.Path); !os.IsNotExist(err) {
			if err != nil {
				return err
			}
			return errors.New("aggregate member path reappeared after approved deletion")
		}
		// --force is limited to an already absent, exact registered checkout. It
		// removes its stale administration entry; no unreviewed tree is deleted.
		if _, err := m.Git(ctx, member.Mirror, "worktree", "remove", "--force", selected); err != nil {
			return err
		}
	}
	return nil
}

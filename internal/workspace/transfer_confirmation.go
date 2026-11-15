package workspace

import (
	"context"
	"errors"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/worktreetransfer"
)

func transferred(b *model.Bench) error {
	if err := workspacepolicy.Validate(b); err != nil {
		return err
	}
	if b.Workspace.Transfer == nil || b.Workspace.Transfer.Phase != "transferred" || b.Lease != nil || b.Workspace.Checkpoint == nil || b.Workspace.Pending != nil {
		return errors.New("workspace has not completed task-ref transfer")
	}
	return nil
}

// ConfirmTransferred verifies only immutable checkpoint authority. The source
// no longer owns task refs, and the destination may legitimately advance them.
// Ordinary Confirm deliberately retains its current-task-ref requirements.
func (l Lifecycle) ConfirmTransferred(ctx context.Context, b *model.Bench) error {
	ctx, cancel, err := l.bounded(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if err = transferred(b); err != nil {
		return err
	}
	if b.Path != filepath.Join(l.Data, "benches", b.Key) {
		return errors.New("transferred workspace root changed")
	}
	for _, path := range []string{l.Data, filepath.Join(l.Data, "benches"), filepath.Join(l.Data, "mirrors")} {
		if err = physicalDirectory(path, false); err != nil {
			return err
		}
	}
	for _, member := range b.Workspace.Members {
		if err = physicalDirectory(member.Mirror, false); err != nil {
			return err
		}
		bare, err := l.git(ctx, member.Mirror, "rev-parse", "--is-bare-repository")
		if err != nil {
			return err
		}
		origin, err := l.git(ctx, member.Mirror, "config", "--null", "--get-all", "remote.origin.url")
		if err != nil {
			return err
		}
		format, err := l.git(ctx, member.Mirror, "rev-parse", "--show-object-format=storage")
		if err != nil {
			return err
		}
		if bare != "true" || origin != member.Repository+"\x00" || format != member.ObjectFormat {
			return errors.New("transferred workspace mirror identity changed")
		}
	}
	c, err := l.coordinator(b)
	if err != nil {
		return err
	}
	manifest, err := workspacepolicy.Manifest(b, b.Workspace.Checkpoint)
	if err != nil {
		return err
	}
	return c.VerifyImmutable(ctx, manifest, b.Workspace.Checkpoint.Seal, b.Workspace.Checkpoint.Digest)
}

// InspectTransferred validates a completed transfer's physical source checkout
// vector, including its detached registrations, without acquiring writer rights.
func (l Lifecycle) InspectTransferred(ctx context.Context, b *model.Bench, clean bool) ([]model.WorkspaceCommit, error) {
	ctx, cancel, err := l.bounded(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if err = transferred(b); err != nil {
		return nil, err
	}
	if err = l.inspectRoot(b, clean); err != nil {
		return nil, err
	}
	vector := make([]model.WorkspaceCommit, 0, len(b.Workspace.Members))
	checker := worktreetransfer.Manager{Git: l.Git, Timeout: l.Timeout}
	for i, member := range b.Workspace.Members {
		cp := b.Workspace.Checkpoint.Members[i]
		checkout, e := filepath.EvalSymlinks(member.Path)
		if e != nil {
			return nil, e
		}
		mirror, e := filepath.EvalSymlinks(member.Mirror)
		if e != nil {
			return nil, e
		}
		if _, err = checker.InspectDetached(ctx, checkout, mirror, "refs/heads/"+member.Branch, cp.Commit); err != nil {
			return nil, err
		}
		if clean {
			out, err := l.git(ctx, member.Path, "status", "--porcelain", "--untracked-files=all")
			if err != nil {
				return nil, err
			}
			if out != "" {
				return nil, model.ErrDirty
			}
		}
		vector = append(vector, cp)
	}
	return vector, nil
}

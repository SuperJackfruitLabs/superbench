package workspace

import (
	"context"
	"errors"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// Confirm checks the registered mirrors and complete remote evidence without
// requiring checkouts that an approved reap may already have partially removed.
// Its caller retains the host lock across confirmation and the guarded action.
func (l Lifecycle) Confirm(ctx context.Context, b *model.Bench) error {
	ctx, cancel, err := l.bounded(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if err := workspacepolicy.Validate(b); err != nil {
		return err
	}
	if b.Path != filepath.Join(l.Data, "benches", b.Key) || b.Workspace.Checkpoint == nil || b.Workspace.Pending != nil {
		return errors.New("workspace requires a complete checkpoint at its registered root")
	}
	for _, path := range []string{l.Data, filepath.Join(l.Data, "benches"), filepath.Join(l.Data, "mirrors")} {
		if err := physicalDirectory(path, false); err != nil {
			return err
		}
	}
	for _, member := range b.Workspace.Members {
		if err := physicalDirectory(member.Mirror, false); err != nil {
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
			return errors.New("workspace mirror identity or object format changed")
		}
	}
	return l.confirm(ctx, b, b.Workspace.Checkpoint)
}

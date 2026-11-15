package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/mirrorstage"
)

// The caller holds the host store lock across the complete clone/publication.
// This adapter never upgrades a cooperative Git command to a fenced writer.
func (m Mirrors) newStage(ctx context.Context, mirror string) (*mirrorstage.Intent, error) {
	state, root, managed := gitcontrol.ConfiguredScope(ctx)
	if !managed {
		return mirrorstage.Create(m.State, filepath.Dir(mirror), mirror)
	}
	actual, e := filepath.EvalSymlinks(m.State)
	if e != nil || state != actual {
		return nil, errors.New("mirror staging control state differs from host state")
	}
	return mirrorstage.CreateManaged(m.State, filepath.Dir(mirror), mirror, root)
}

func (m Mirrors) verifyStage(p *mirrorstage.Intent) error {
	saved, root, e := mirrorstage.Load(m.State, p.Parent, p.ID)
	if e != nil {
		return e
	}
	if *saved != *p {
		return errors.New("mirror stage reservation changed before publication")
	}
	return mirrorstage.CheckRoot(*saved, *root)
}

func (m Mirrors) clone(ctx context.Context, repo, mirror string) error {
	stage, e := m.newStage(ctx, mirror)
	if e != nil {
		return e
	}
	// Never recursively clean a failed clone. Its controller may have died before
	// fencing, or its surviving files may need explicit operator review.
	if e = m.verifyStage(stage); e != nil {
		return e
	}
	candidate := filepath.Join(stage.Stage, "repo.git")
	if _, e = git(ctx, m.State, "clone", "--bare", "--no-hardlinks", "--", repo, candidate); e != nil {
		return e
	}
	if e = m.verifyStage(stage); e != nil {
		return e
	}
	if _, e = os.Lstat(mirror); !os.IsNotExist(e) {
		return errors.New("mirror publication target appeared; preserve staging data")
	}
	if e = os.Rename(candidate, mirror); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(mirror))
	if e != nil {
		return e
	}
	e = dir.Sync()
	closeErr := dir.Close()
	if e = errors.Join(e, closeErr); e != nil {
		return e
	}
	if stage.ControlRoot == "" {
		// Successful ordinary Git does not prove all cooperative descendants
		// stopped. Preserve the empty stage and journal rather than infer a fence.
		return nil
	}
	manager := mirrorstage.Manager{
		State: m.State, Parent: stage.Parent,
		Within: func(fn func() error) error { return fn() }, // host lock is already held
		VerifyControl: func(ctx context.Context, state, root string) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			configuredState, configuredRoot, ok := gitcontrol.ConfiguredScope(ctx)
			if !ok || state != configuredState || root != configuredRoot || !gitcontrol.Available(root) {
				return errors.New("published mirror cleanup requires its original managed Git control")
			}
			return gitcontrol.Recover(state)
		},
	}
	return manager.CleanupPublished(ctx, stage.ID)
}

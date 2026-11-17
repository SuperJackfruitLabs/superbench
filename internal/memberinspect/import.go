package memberinspect

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// Import makes each Fresh member's checkpoint commit present in its daemon
// mirror before the daemon pushes from that mirror. The payload exports a
// bounded bundle of only what the mirror lacks; the daemon verifies it and
// fetches its objects without creating a ref. No payload file is read, and a
// legacy checkout already shares its mirror's objects, so it does nothing.
func (i Inspector) Import(ctx context.Context, state string, b *model.Bench, vector []model.WorkspaceCommit) error {
	if i.Tree == nil {
		return nil
	}
	if len(vector) != len(b.Workspace.Members) {
		return errors.New("fresh checkpoint vector differs from its members")
	}
	for n, member := range b.Workspace.Members {
		head := vector[n].Commit
		if vector[n].Name != member.Name || !model.ValidCommit(head) {
			return errors.New("fresh checkpoint vector differs from its members")
		}
		if i.present(ctx, member.Mirror, head) {
			continue
		}
		candidates := []string{member.Source}
		if cp := b.Workspace.Checkpoint; cp != nil && len(cp.Members) == len(vector) {
			candidates = append(candidates, cp.Members[n].Commit)
		}
		var exclude []string
		for _, commit := range candidates {
			if model.ValidCommit(commit) && i.present(ctx, member.Mirror, commit) {
				exclude = append(exclude, commit)
			}
		}
		raw, err := i.Tree.Export(ctx, b, member.Name, exclude)
		if err != nil {
			return err
		}
		if err = i.fetch(ctx, state, b, member, raw); err != nil {
			return err
		}
		if !i.present(ctx, member.Mirror, head) {
			return errors.New("fresh checkpoint bundle did not carry the member HEAD")
		}
	}
	return nil
}

func (i Inspector) present(ctx context.Context, mirror, commit string) bool {
	_, err := i.git(ctx, mirror, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

// fetch stages the bundle in daemon-private state, never in bench data.
func (i Inspector) fetch(ctx context.Context, state string, b *model.Bench, member model.WorkspaceMember, raw []byte) error {
	dir := filepath.Join(state, "fresh-imports")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.Join(errors.New("fresh import staging must be a private directory"), err)
	}
	f, err := os.CreateTemp(dir, b.Key+"-"+member.Name+"-*.bundle")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	_, err = f.Write(raw)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if _, err = i.git(ctx, member.Mirror, "bundle", "verify", path); err != nil {
		return err
	}
	_, err = i.git(ctx, member.Mirror, "fetch", "--no-tags", "--no-write-fetch-head", path, "HEAD")
	return err
}

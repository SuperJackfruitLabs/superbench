package bench

import (
	"context"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func (s Store) mirrors() repository.Mirrors {
	return repository.Mirrors{State: s.State, Data: s.Data, Timeout: s.MirrorTimeout}
}
func (s Store) prepareMirror(repo string) (string, error) {
	return s.mirrors().Prepare(context.Background(), repo)
}
func (s Store) refreshMirror(ctx context.Context, b *Bench) error {
	if b.Mirror != s.mirrorPath(b.Repo) {
		return errors.New("registered mirror path differs")
	}
	return s.mirrors().RefreshCheckout(ctx, b.Repo, repository.Checkout{Path: b.Path, Branch: b.Branch, Mirror: b.Mirror})
}

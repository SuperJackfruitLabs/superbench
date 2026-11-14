package bench

import (
	"context"
	"database/sql"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

func (s Store) Setup(ctx context.Context, key, holder string, offline bool) (*RunResult, error) {
	return s.run(ctx, key, holder, nil, true, func(b *Bench, tx *sql.Tx, c *setup.Config) ([]string, func(*sql.Tx, string) error, error) {
		return s.setupPlan(b, tx, c, offline)
	})
}
func (s Store) setupPlan(b *Bench, tx *sql.Tx, c *setup.Config, offline bool) ([]string, func(*sql.Tx, string) error, error) {
	if c == nil || !c.Hooks() {
		if e := setup.Invalidate(tx, b.Key); e != nil {
			return nil, nil, e
		}
		args, e := setup.FrozenCommandFor(b.Path, cachepolicy.Select(s.Data, b.Path, c.CacheSettings()), offline)
		return args, nil, e
	}
	return s.configuredSetup(b, tx, c, offline)
}
func (s Store) configuredSetup(b *Bench, tx *sql.Tx, c *setup.Config, offline bool) ([]string, func(*sql.Tx, string) error, error) {
	key := b.Key
	if e := setup.Admit(tx, key); e != nil {
		return nil, nil, e
	}
	done, e := setup.Completed(tx, key, c.Digest)
	if e != nil {
		return nil, nil, e
	}
	if !done {
		if e = setup.Invalidate(tx, key); e != nil {
			return nil, nil, e
		}
	}
	finish := func(tx *sql.Tx, runID string) error {
		if e := setup.Verify(b.Path, c.Digest); e != nil {
			return e
		}
		return setup.Record(tx, key, c.Digest, runID)
	}
	return c.Command(done, offline), finish, nil
}
func (s Store) setupCommand(path string, offline bool) ([]string, error) {
	return setup.FrozenCommand(path, s.Data, offline)
}
func (s Store) cacheEnvironment(env []string) []string { return execution.Environment(s.Data, env) }

func (s Store) cacheEnvironmentFor(b *Bench, c *setup.Config, env []string) ([]string, error) {
	if c.CacheSettings().Private() {
		tracked, e := git(b.Path, "ls-files", "--", ".bench/cache")
		if e != nil || tracked != "" {
			return nil, errors.New("checkout-local cache namespace must be untracked and Git-readable")
		}
	}
	paths, e := cachepolicy.Prepare(s.Data, b.Path, c.CacheSettings())
	if e != nil {
		return nil, e
	}
	return execution.EnvironmentFor(paths, env), nil
}

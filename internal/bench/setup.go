package bench

import (
	"context"
	"database/sql"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

func (s Store) Setup(ctx context.Context, key, holder string, offline bool) (*RunResult, error) {
	return s.run(ctx, key, holder, nil, true, func(b *Bench, tx *sql.Tx) ([]string, func(*sql.Tx, string) error, error) {
		c, e := setup.Read(b.Path)
		if e != nil {
			return nil, nil, e
		}
		if c == nil || !c.Hooks() {
			if e = setup.Invalidate(tx, key); e != nil {
				return nil, nil, e
			}
			args, e := s.setupCommand(b.Path, offline)
			return args, nil, e
		}
		return s.configuredSetup(b, tx, c, offline)
	})
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

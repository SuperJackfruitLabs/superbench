package bench

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

func (s Store) Create(key string, o Options, recovering bool) (*Bench, error) {
	return s.CreateContext(context.Background(), key, o, recovering)
}

// CreateColdContext is explicit local administration and legacy API behavior.
// It publishes only the pinned checkout; it never runs repository setup.
func (s Store) CreateColdContext(ctx context.Context, key string, o Options, recovering bool) (*Bench, error) {
	if err := s.RequireLegacy(key); err != nil {
		return nil, err
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	s, ctx, e := s.withControls(ctx, false)
	if e != nil {
		return nil, e
	}
	b, _, e := s.createRegistered(ctx, key, o, recovering, false)
	return b, e
}
func (s Store) CreateContext(ctx context.Context, key string, o Options, recovering bool) (*Bench, error) {
	if err := s.RequireLegacy(key); err != nil {
		return nil, err
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	s, ctx, e := s.withControls(ctx, false)
	if e != nil {
		return nil, e
	}
	b, warming, e := s.createRegistered(ctx, key, o, recovering, true)
	if e != nil || !warming {
		return b, e
	}
	if e = ctx.Err(); e != nil {
		return b, fmt.Errorf("checkout registered; initial setup still holds its lease: %w", e)
	}
	r, e := s.runStage(ctx, key, b.Lease.Holder, nil, true, func(b *Bench, tx *sql.Tx, c *setup.Config) ([]string, func(*sql.Tx, string) error, error) {
		return s.setupPlan(b, tx, c, false)
	}, stageOptions{prior: permit(b)})
	if r != nil {
		b = r.Bench
	}
	if e != nil {
		return b, fmt.Errorf("checkout preserved; reconcile the initial setup lease: %w", e)
	}
	return b, nil
}
func (s Store) creationSetupNeeded(b *Bench) (bool, error) {
	c, e := setup.Read(b.Path)
	if e != nil {
		return true, e
	}
	if e = setup.MatchQuota(c, b.RequestedQuotaBytes); e != nil {
		return true, e
	}
	if c != nil && c.Hooks() {
		return true, nil
	}
	_, e = s.setupCommand(b.Path, false)
	if errors.Is(e, setup.ErrNoLockfile) {
		return false, nil
	}
	return true, e
}

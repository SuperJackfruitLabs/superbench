package bench

import (
	"context"
	"database/sql"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

type stageOptions struct {
	prior     *stagePermit
	preparing *bool
}
type stagePermit struct{ Token, Continuation, ExecutionID string }

func executionID(l *Lease) string {
	if l.Execution != nil {
		return l.Execution.ID
	}
	return ""
}
func permit(b *Bench) *stagePermit {
	return &stagePermit{b.Lease.Token, b.Lease.Continuation, executionID(b.Lease)}
}
func (p stagePermit) check(b *Bench) error {
	if e := owned(b, p.Token); e != nil {
		return e
	}
	if p.Continuation == "" || b.Lease.Continuation != p.Continuation || executionID(b.Lease) != p.ExecutionID {
		return ErrLease
	}
	if b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped" {
		return ErrNotFenced
	}
	return nil
}
func (s Store) retainExecution(tx *sql.Tx, b *Bench) error {
	if b.Lease.Execution == nil {
		return nil
	}
	if e := s.ensureStopped(b); e != nil {
		return e
	}
	r, e := history.Get(tx, b.Lease.Execution.ID)
	if e != nil {
		return e
	}
	if len(b.Lease.RetainedRuns) >= 8 || r.Key != b.Key || r.File == nil || r.ExitCode == nil || *r.ExitCode != 0 || !r.ReleasedAt.IsZero() {
		return errors.New("preparation has no retained sealed successful outcome")
	}
	b.Lease.RetainedRuns = append(b.Lease.RetainedRuns, r.ID)
	b.Lease.Execution = nil
	return nil
}

func (s Store) Run(ctx context.Context, key, holder string, args []string, checkpoint bool) (*RunResult, error) {
	if len(args) == 0 {
		return nil, errors.New("command required")
	}
	prepared := false
	digest := ""
	first, e := s.runStage(ctx, key, holder, args, checkpoint, func(b *Bench, tx *sql.Tx, configuration *setup.Config) ([]string, func(*sql.Tx, string) error, error) {
		c := configuration
		if c == nil || !c.Hooks() {
			return args, nil, setup.Invalidate(tx, key)
		}
		prepared = true
		digest = c.Digest
		return s.configuredSetup(b, tx, c, false)
	}, stageOptions{preparing: &prepared})
	if e != nil || !prepared {
		return first, e
	}
	result, e := s.runStage(ctx, key, holder, args, checkpoint, func(b *Bench, _ *sql.Tx, configuration *setup.Config) ([]string, func(*sql.Tx, string) error, error) {
		return args, nil, setup.Match(configuration, digest)
	}, stageOptions{prior: permit(first.Bench)})
	if result == nil {
		return first, e
	}
	return result, e
}

func (s Store) Lease(key, holder string) (*Bench, error) {
	return s.LeaseContext(context.Background(), key, holder)
}

// RepairLease is local administration: acquire a cooperative lease without
// executing broken repository setup. It is deliberately absent from the RPC API.
func (s Store) RepairLease(key, holder string) (*Bench, error) {
	b, _, e := s.acquireLease(context.Background(), key, holder, false)
	return b, e
}
func (s Store) LeaseContext(ctx context.Context, key, holder string) (*Bench, error) {
	if err := s.RequireLegacy(key); err != nil {
		return nil, err
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	s, ctx, err := s.withControls(ctx, false)
	if err != nil {
		return nil, err
	}
	b, c, e := s.acquireLease(ctx, key, holder, true)
	if e != nil || c == nil {
		return b, e
	}
	preparing := true
	r, e := s.runStage(ctx, key, holder, nil, false, func(b *Bench, tx *sql.Tx, configuration *setup.Config) ([]string, func(*sql.Tx, string) error, error) {
		if e := setup.Match(configuration, c.Digest); e != nil {
			return nil, nil, e
		}
		return s.configuredSetup(b, tx, c, false)
	}, stageOptions{prior: permit(b), preparing: &preparing})
	if r != nil {
		b = r.Bench
	}
	if e != nil {
		return b, e
	}
	p := permit(b)
	e = s.executionGate(key, func() error {
		return s.transactionSQL(func(db database, tx *sql.Tx) error {
			current, e := get(db, key)
			if e != nil {
				return e
			}
			if e = p.check(current); e != nil {
				return e
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			if e = s.retainExecution(tx, current); e != nil {
				return e
			}
			current.Lease.Continuation = ""
			copy := *current
			b = &copy
			return nil
		})
	})
	return b, e
}

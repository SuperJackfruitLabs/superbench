package bench

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/localprep"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Execution = execution.Execution
type RunResult struct {
	Execution    *Execution
	Bench        *Bench
	ExitCode     int
	LogTruncated bool
}
type executionIdentity = execution.Identity
type executionBackend interface {
	Create(root, id string) (executionIdentity, error) // returns boot identity; never start outside scope
	Start(*exec.Cmd, *Execution) error
	Fence(*Execution) error // succeeds only after all scope descendants have stopped
	Empty(*Execution) error
}

func (s Store) backend() executionBackend {
	if s.executor != nil {
		return s.executor
	}
	return execution.Host{}
}
func nonce() (string, error)                 { return model.Nonce() }
func newLease(holder string) (*Lease, error) { return model.NewLease(holder) }

// The execution gate linearizes start and stop independently from the global
// state lock. It is never held while waiting for a command's execution to finish.
func (s Store) executionGate(key string, fn func() error) error {
	return stageexec.Gate(s.State, key, fn)
}

type commandPlan func(*Bench, *sql.Tx, *setup.Config) ([]string, func(*sql.Tx, string) error, error)

func (s Store) run(ctx context.Context, key, holder string, args []string, checkpoint bool, plan commandPlan) (*RunResult, error) {
	return s.runStage(ctx, key, holder, args, checkpoint, plan, stageOptions{})
}
func (s Store) runStage(ctx context.Context, key, holder string, args []string, checkpoint bool, plan commandPlan, options stageOptions) (*RunResult, error) {
	if err := s.RequireLegacy(key); err != nil {
		return nil, err
	}
	if s.executor == nil && !execution.Supported {
		return nil, errors.New("fenced execution requires Linux cgroup v2; use cooperative lease/checkpoint commands on this host")
	}
	var finish func(*sql.Tx, string) error
	if len(args) == 0 && plan == nil {
		return nil, errors.New("command required")
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	s, ctx, err := s.withControls(ctx, true)
	if err != nil {
		return nil, err
	}
	id, err := nonce()
	if err != nil {
		return nil, err
	}
	root := s.CgroupRoot
	if root == "" {
		root = "/sys/fs/cgroup/superbench"
	}
	execution := &Execution{ID: id, Root: root, Phase: "starting", Log: filepath.Join(s.State, "run-"+id+".log")}
	scopeCreated := false
	var b *Bench
	var environment []string
	acquire := func() error {
		return s.transactionSQL(func(db database, tx *sql.Tx) error {
			original, e := get(db, key)
			if e != nil {
				return e
			}
			if e := retention.Directory(s.State); e != nil {
				return e
			}
			if e := s.admit(); e != nil {
				return e
			}
			if original.ArchivedAt != "" {
				return ErrArchived
			}
			if options.prior != nil {
				if e = options.prior.check(original); e != nil {
					return e
				}
				if e = s.retainExecution(tx, original); e != nil {
					return e
				}
			} else if original.Lease != nil {
				return ErrBusy
			}

			if e := s.ensureBenchQuota(original); e != nil {
				return e
			}
			if options.prior == nil {
				if e = s.refreshMirror(ctx, original); e != nil {
					return e
				}
			}
			limit, e := s.reserveLog(db)
			if e != nil {
				return e
			}
			execution.LogLimitBytes = limit
			configuration, e := setup.Read(original.Path)
			if e != nil {
				return e
			}
			if e = setup.MatchQuota(configuration, original.RequestedQuotaBytes); e != nil {
				return e
			}
			if plan != nil {
				args, finish, e = plan(original, tx, configuration)
				if e != nil {
					return e
				}
				if len(args) == 0 {
					return errors.New("command plan is empty")
				}
			}
			environment, e = s.cacheEnvironmentFor(original, configuration, os.Environ())
			if e != nil {
				return e
			}
			lease := original.Lease
			if lease == nil {
				lease, e = newLease(holder)
				if e != nil {
					return e
				}
			}
			if options.preparing != nil && *options.preparing {
				if lease.Continuation == "" {
					lease.Continuation, e = nonce()
					if e != nil {
						return e
					}
				}
			} else {
				lease.Continuation = ""
			}

			if e := ctx.Err(); e != nil {
				return e
			}
			identity, e := s.backend().Create(root, id)
			if e != nil {
				return e
			}
			scopeCreated = true
			execution.BootID, execution.HostID = identity.BootID, identity.HostID
			original.Lease = lease
			original.Lease.Execution = execution
			if e = history.Start(tx, history.Record{ID: id, Key: key, Holder: holder, StartedAt: time.Now().UTC()}); e != nil {
				return e
			}
			copy := *original
			b = &copy
			return nil
		})
	}
	if options.prior != nil {
		err = s.executionGate(key, acquire)
	} else {
		err = acquire()
	}
	if err != nil {
		if scopeCreated {
			_ = s.backend().Fence(execution)
		}
		return nil, err
	}
	continuation := ""
	if options.preparing != nil && *options.preparing {
		continuation = b.Lease.Continuation
		checkpoint = false
	}
	result := &RunResult{Bench: b, ExitCode: -1, Execution: execution}
	stage, err := (stageexec.Runner{
		Backend: s.backend(),
		Start: func(ctx context.Context, cmd *exec.Cmd, execution *Execution) error {
			return s.startExecution(ctx, key, b, cmd, execution)
		},
		Stop: func() (*Execution, error) {
			stopped, err := s.stop(key, b.Lease.Token, false, continuation, true)
			if err != nil {
				return nil, err
			}
			result.Bench = stopped
			return stopped.Lease.Execution, nil
		},
		Cancel: func() { _, _ = s.Stop(key, b.Lease.Token, false) },
		Seal: func(capture *output.Capture, exit *int, truncated bool) error {
			return s.sealRunLog(id, capture, exit, truncated)
		},
	}).Run(ctx, stageexec.Plan{Execution: execution, Dir: b.Path, Args: args, Environment: environment})
	if stage != nil {
		result.Execution, result.ExitCode, result.LogTruncated = stage.Execution, stage.ExitCode, stage.LogTruncated
	}
	if err != nil {
		return result, err
	}
	if finish != nil {
		err = s.transactionSQL(func(records database, tx *sql.Tx) error {
			current, e := get(records, key)
			if e != nil {
				return e
			}
			if e = owned(current, b.Lease.Token); e != nil {
				return e
			}
			if continuation != "" && current.Lease.Continuation != continuation {
				return ErrLease
			}
			if e = s.ensureStopped(current); e != nil {
				return e
			}
			return finish(tx, id)
		})
		if err != nil {
			return result, fmt.Errorf("setup success save failed; stopped lease retained: %w", err)
		}
	}
	if !checkpoint {
		return result, nil
	}
	cp, err := s.Checkpoint(key, b.Lease.Token)
	if err != nil {
		return result, fmt.Errorf("checkpoint failed; stopped lease retained: %w", err)
	}
	result.Bench.Checkpoint = cp.Checkpoint
	if err = s.release(key, b.Lease.Token, true); err != nil {
		return result, err
	}
	result.Bench.Lease = nil
	return result, nil
}

// Admin is for a local operator with access to the private state directory.
// Network adapters must authorize administrative access before calling it.
func (s Store) Stop(key, token string, admin bool) (*Bench, error) {
	return s.stop(key, token, admin, "", false)
}
func (s Store) stop(key, token string, admin bool, continuation string, internal bool) (*Bench, error) {
	if err := s.RequireLegacy(key); err != nil {
		return nil, err
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	var result *Bench
	err := s.executionGate(key, func() error {
		var prior *Bench
		if err := s.read(func(db database) error {
			b, e := get(db, key)
			if e != nil {
				return e
			}
			if b.Lease == nil {
				return errors.New("bench is not leased")
			}
			if !admin {
				if e = owned(b, token); e != nil {
					return e
				}
			}
			if continuation != "" && b.Lease.Continuation != continuation {
				return ErrLease
			}
			if b.Lease.Execution == nil && b.Lease.Continuation == "" {
				return errors.New("lease has no supervised execution; stop its writer manually")
			}

			prior = b
			return nil
		}); err != nil {
			return err
		}
		if prior.Lease.Execution != nil {
			if err := s.backend().Fence(prior.Lease.Execution); err != nil {
				return err
			}
		}

		return s.transaction(func(db database) error {
			b, e := get(db, key)
			if e != nil {
				return e
			}
			if e = owned(b, prior.Lease.Token); e != nil {
				return e
			}
			if continuation != "" && b.Lease.Continuation != continuation {
				return ErrLease
			}
			if executionID(b.Lease) != executionID(prior.Lease) {
				return errors.New("execution changed during stop")
			}
			if b.Lease.Execution != nil {
				b.Lease.Execution.Phase = "stopped"
			}
			if continuation == "" {
				b.Lease.Continuation = ""
			}
			if !internal {
				b.Lease.StopRequested = true
			}
			copy := *b
			result = &copy
			return nil
		})
	})
	return result, err
}

func (s Store) ensureStopped(b *Bench) error {
	if err := localprep.CheckHandoff(s.State, b); err != nil {
		return err
	}
	if b.Lease == nil || b.Lease.Execution == nil {
		return nil
	}
	if b.Lease.Execution.Phase != "stopped" {
		return ErrNotFenced
	}
	return s.backend().Empty(b.Lease.Execution)
}

func (s Store) startExecution(ctx context.Context, key string, b *Bench, cmd *exec.Cmd, execution *Execution) error {
	return s.executionGate(key, func() error {
		return s.transaction(func(db database) error {
			current, e := get(db, key)
			if e != nil {
				return e
			}
			if e = owned(current, b.Lease.Token); e != nil {
				return e
			}
			if current.Lease.Execution == nil || current.Lease.Execution.ID != execution.ID || current.Lease.Execution.Phase != "starting" {
				return errors.New("execution was already stopped or replaced")
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			if e = s.backend().Start(cmd, execution); e != nil {
				return e
			}
			current.Lease.Execution.Phase = "running"
			current.Lease.Execution.PID = cmd.Process.Pid
			return nil
		})
	})
}

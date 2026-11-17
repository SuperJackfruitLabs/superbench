package workspaceprep

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadstage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

type RunResult struct {
	Bench        *model.Bench
	Execution    *execution.Execution
	ExitCode     int
	LogTruncated bool
	IO           *finiteio.Output // only a profiled run's sealed exact stdout
}

// FiniteRun is a profiled run's copied input, stdout bound and helper transport.
type FiniteRun = payloadstage.FiniteRun

func (m Manager) operational(b *model.Bench) error {
	if b.Workspace.Phase != "ready" {
		return errors.New("workspace payload requires a ready workspace lease")
	}
	return m.preparable(b)
}
func payloadDir(b *model.Bench, selector string) (string, error) {
	if selector == "" {
		return b.Path, nil
	}
	for _, member := range b.Workspace.Members {
		if member.Name == selector {
			return member.Path, nil
		}
	}
	return "", errors.New("workspace member selector is not registered")
}
func (m Manager) prepared(tx *sql.Tx, b *model.Bench) error {
	if err := m.Prepared(b); err != nil {
		return err
	}
	for i := range b.Workspace.Preparation {
		if err := verifyProof(tx, b, &b.Workspace.Preparation[i]); err != nil {
			return err
		}
	}
	return nil
}
func (m Manager) inspectPayload(ctx context.Context, b *model.Bench) error {
	_, err := m.lifecycle().Inspect(ctx, b, false)
	return err
}
func (m Manager) payloadEnvironment(ctx context.Context, b *model.Bench, selector string) ([]string, error) {
	if selector != "" {
		for _, member := range b.Workspace.Members {
			if member.Name == selector {
				c, digest, err := freshtree.Inputs(ctx, m.Fresh, m.State, b, member)
				if err != nil {
					return nil, err
				}
				return m.environment(ctx, b, memberPlan{member: member, config: c, digest: digest})
			}
		}
		return nil, errors.New("workspace member selector is not registered")
	}
	// Omitted repository_name means the aggregate root. No member implicitly
	// supplies the root's configuration; its caches use the fixed shared policy.
	if fresh, err := freshtree.Selected(m.Fresh, m.State, b); err != nil || fresh {
		return nil, err // a Fresh root's payload gets only the helper's environment
	}
	paths, err := operationcontrol.Value(ctx, func() (cachepolicy.Paths, error) { return cachepolicy.Prepare(m.Data, b.Path, cachepolicy.Settings{}) })
	if err != nil {
		return nil, err
	}
	return execution.EnvironmentFor(paths, os.Environ()), nil
}

// Run prepares all current members before a payload under the same ready lease.
// It leaves stopped execution ownership held for checkpoint/release or explicit
// continuation; payload history never becomes member preparation evidence.
// A finite plan keeps these rules; its IO comes only from payloadstage.Complete.
func (m Manager) Run(ctx context.Context, key, token, selector string, args []string, offline bool, finite FiniteRun) (result *RunResult, operationErr error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := payloadstage.Arguments(args); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := m.current(key)
	if err != nil {
		return nil, err
	}
	if _, err := owned(benchstate.Database{key: b}, key, token); err != nil {
		return nil, err
	}
	if err := m.operational(b); err != nil {
		return nil, err
	}
	defer func() {
		current := b
		if result != nil {
			current = result.Bench
		}
		m.reconcileCancellation(ctx, key, token, &current, &operationErr)
		if ctx.Err() != nil {
			if result == nil {
				result = &RunResult{ExitCode: -1}
			}
			result.Bench, result.IO = current, nil
		}
	}()
	if _, err := payloadDir(b, selector); err != nil {
		return nil, err
	}
	if _, native := m.Backend.(execution.Host); native && !execution.Supported {
		return nil, errors.New("workspace payload requires supervised execution on this host")
	}
	b, err = m.Prepare(ctx, key, token, "", offline)
	result = &RunResult{Bench: b, ExitCode: -1}
	if err != nil {
		return result, err
	}
	var stage *execution.Execution
	var prepare stageexec.PrepareTransport
	var env []string
	var dir, continuation string
	created := false
	err = m.Gate(key, func() error {
		return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b, err := owned(rows, key, token)
			if err != nil {
				return err
			}
			if err := m.operational(b); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := m.stopped(b); err != nil {
				return err
			}
			if err := payloadstage.Check(finite); err != nil {
				return err
			}
			if err := m.inspectPayload(ctx, b); err != nil {
				return err
			}
			if err := m.prepared(tx, b); err != nil {
				return err
			}
			dir, err = payloadDir(b, selector)
			if err != nil {
				return err
			}
			if err := retain(tx, b); err != nil {
				return err
			}
			if err := m.Admit(); err != nil {
				return err
			}
			if err := m.quota(b); err != nil {
				return err
			}
			limit, err := m.ReserveLog(rows)
			if err != nil {
				return err
			}
			if finite == nil { // The finite helper builds its own fixed environment.
				if env, err = m.payloadEnvironment(ctx, b, selector); err != nil {
					return err
				}
			}
			id, err := model.Nonce()
			if err != nil {
				return err
			}
			root := m.CgroupRoot
			if root == "" {
				root = "/sys/fs/cgroup/superbench"
			}
			stage = &execution.Execution{ID: id, Root: root, Phase: "starting", Log: filepath.Join(m.State, "run-"+id+".log"), LogLimitBytes: limit}
			identity, err := operationcontrol.Value(ctx, func() (execution.Identity, error) { return m.Backend.Create(root, id) })
			if err != nil {
				return err
			}
			created = true
			stage.HostID, stage.BootID = identity.HostID, identity.BootID
			b.Lease.Execution = stage
			b.Workspace.Stage = nil
			if prepare, err = payloadstage.Transport(finite, b, selector); err == nil && prepare == nil {
				prepare, err = freshtree.StageTransport(m.Fresh, m.State, b, selector)
			}
			if err != nil {
				return err
			}
			continuation = b.Lease.Continuation
			return operationcontrol.StartHistory(ctx, tx, b, history.Record{ID: id, Key: key, Holder: b.Lease.Holder, StartedAt: time.Now().UTC()})
		})
	})
	if err != nil {
		if created {
			_ = m.Backend.Fence(stage)
		}
		var readErr error
		result.Bench, readErr = m.ownedCurrent(key, token)
		return result, errors.Join(err, readErr)
	}
	result.Execution = stage
	runner := stageexec.Runner{Prepare: prepare, Open: func() (*output.Capture, error) { return m.openCapture(ctx, key, token, continuation, stage) }, Backend: m.Backend, StartPrepared: func(ctx context.Context, cmd *exec.Cmd, e *execution.Execution, launch stageexec.Launch) error {
		return m.Gate(key, func() error {
			return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
				b, err := owned(rows, key, token)
				if err != nil {
					return err
				}
				if err := m.operational(b); err != nil {
					return err
				}
				if b.Lease.Continuation != continuation || b.Lease.Execution == nil || b.Lease.Execution.ID != e.ID || b.Lease.Execution.Phase != "starting" || b.Workspace.Stage != nil {
					return errors.New("workspace payload was stopped or replaced")
				}
				if err := payloadstage.Check(finite); err != nil {
					return err
				}
				if err := m.inspectPayload(ctx, b); err != nil {
					return err
				}
				if err := m.prepared(tx, b); err != nil {
					return err
				}
				actual, err := payloadDir(b, selector)
				if err != nil || actual != dir {
					return errors.New("workspace payload directory changed")
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := operationcontrol.Effect(ctx, func() error { return launch(func() error { return m.Backend.Start(cmd, e) }) }); err != nil {
					return err
				}
				b.Lease.Execution.Phase = "running"
				b.Lease.Execution.PID = cmd.Process.Pid
				return nil
			})
		})
	}, Stop: func() (*execution.Execution, error) {
		return m.completeExecution(key, token, stage)
	}, Cancel: func() { _, _ = m.Stop(key, token, false) }, Seal: func(c *output.Capture, exit *int, truncated bool) error { return m.Seal(stage.ID, c, exit, truncated) }}
	outcome, runErr := runner.Run(ctx, stageexec.Plan{Execution: stage, Dir: dir, Args: append([]string(nil), args...), Environment: env, ResultLimitBytes: payloadstage.Limit(finite)})
	if outcome != nil {
		result.Execution, result.ExitCode, result.LogTruncated = outcome.Execution, outcome.ExitCode, outcome.LogTruncated
	}
	result.Bench, err = m.ownedCurrent(key, token)
	if err == nil && result.Bench != nil && result.Bench.Lease != nil && (result.Bench.Lease.StopRequested || result.Bench.Lease.Continuation != continuation) {
		err = errors.New("workspace payload continuation was revoked; stopped outcome retained")
	}
	// Known nonzero exits keep exact stdout; any other failure leaves Output nil.
	if finite != nil && err == nil && ctx.Err() == nil && (runErr == nil || outcome != nil && outcome.Output != nil) {
		result.IO, err = payloadstage.Complete(finite, stage, outcome, result.Bench)
	}
	return result, errors.Join(runErr, err)
}

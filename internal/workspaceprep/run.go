package workspaceprep

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

type RunResult struct {
	Bench        *model.Bench
	Execution    *execution.Execution
	ExitCode     int
	LogTruncated bool
}

func (m Manager) operational(b *model.Bench) error {
	if b.Workspace.Phase != "ready" {
		return errors.New("workspace payload requires a ready workspace lease")
	}
	return m.preparable(b)
}
func payloadArgs(args []string) error {
	if len(args) == 0 || len(args) > 128 || args[0] == "" {
		return errors.New("workspace payload requires 1-128 arguments and a nonempty executable")
	}
	for _, arg := range args {
		if len(arg) > 8192 || !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return errors.New("invalid workspace payload argument")
		}
	}
	return nil
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
func prepared(tx *sql.Tx, b *model.Bench) error {
	if err := CheckPrepared(b); err != nil {
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
	_, err := (workspace.Lifecycle{State: m.State, Data: m.Data, Git: m.Git}).Inspect(ctx, b, false)
	return err
}
func (m Manager) payloadEnvironment(ctx context.Context, b *model.Bench, selector string) ([]string, error) {
	if selector != "" {
		for _, member := range b.Workspace.Members {
			if member.Name == selector {
				c, digest, err := inputs(member.Path)
				if err != nil {
					return nil, err
				}
				return m.environment(ctx, memberPlan{member: member, config: c, digest: digest})
			}
		}
		return nil, errors.New("workspace member selector is not registered")
	}
	// Omitted repository_name means the aggregate root. No member implicitly
	// supplies the root's configuration; its caches use the fixed shared policy.
	paths, err := operationcontrol.Value(ctx, func() (cachepolicy.Paths, error) { return cachepolicy.Prepare(m.Data, b.Path, cachepolicy.Settings{}) })
	if err != nil {
		return nil, err
	}
	return execution.EnvironmentFor(paths, os.Environ()), nil
}

// Run prepares all current members before a payload under the same ready lease.
// It leaves stopped execution ownership held for checkpoint/release or explicit
// continuation; payload history never becomes member preparation evidence.
func (m Manager) Run(ctx context.Context, key, token, selector string, args []string, offline bool) (result *RunResult, operationErr error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := payloadArgs(args); err != nil {
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
			result.Bench = current
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
			if err := m.inspectPayload(ctx, b); err != nil {
				return err
			}
			if err := prepared(tx, b); err != nil {
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
			if err := m.Quota(b); err != nil {
				return err
			}
			limit, err := m.ReserveLog(rows)
			if err != nil {
				return err
			}
			env, err = m.payloadEnvironment(ctx, b, selector)
			if err != nil {
				return err
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
	runner := stageexec.Runner{Open: func() (*output.Capture, error) { return m.openCapture(ctx, key, token, continuation, stage) }, Backend: m.Backend, Start: func(ctx context.Context, cmd *exec.Cmd, e *execution.Execution) error {
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
				if err := m.inspectPayload(ctx, b); err != nil {
					return err
				}
				if err := prepared(tx, b); err != nil {
					return err
				}
				actual, err := payloadDir(b, selector)
				if err != nil || actual != cmd.Dir {
					return errors.New("workspace payload directory changed")
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := operationcontrol.Effect(ctx, func() error { return m.Backend.Start(cmd, e) }); err != nil {
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
	outcome, runErr := runner.Run(ctx, stageexec.Plan{Execution: stage, Dir: dir, Args: append([]string(nil), args...), Environment: env})
	if outcome != nil {
		result.Execution, result.ExitCode, result.LogTruncated = outcome.Execution, outcome.ExitCode, outcome.LogTruncated
	}
	result.Bench, err = m.ownedCurrent(key, token)
	if err == nil && result.Bench != nil && result.Bench.Lease != nil && (result.Bench.Lease.StopRequested || result.Bench.Lease.Continuation != continuation) {
		err = errors.New("workspace payload continuation was revoked; stopped outcome retained")
	}
	return result, errors.Join(runErr, err)
}

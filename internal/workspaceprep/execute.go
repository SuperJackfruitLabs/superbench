package workspaceprep

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

func (m Manager) execute(ctx context.Context, key, token string, plan memberPlan, offline bool) error {
	var args, env []string
	var continuation string
	var stage *execution.Execution
	created, noCommand := false, false
	err := m.Gate(key, func() error {
		return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b, err := owned(rows, key, token)
			if err != nil {
				return err
			}
			if err = initializing(b); err != nil {
				return err
			}
			if b.Lease.Execution != nil {
				return errors.New("workspace already has an execution")
			}
			if err = m.inspectMember(ctx, b, plan.member.Name); err != nil {
				return err
			}
			_, digest, err := inputs(plan.member.Path)
			if err != nil || digest != plan.digest {
				return errors.New("workspace preparation inputs changed before launch")
			}
			if plan.config != nil && plan.config.Hooks() {
				done, e := setup.Completed(tx, receipt(b, plan.member.Name), plan.digest)
				if e != nil {
					return e
				}
				args = plan.config.Command(done, offline)
			} else {
				args, err = setup.FrozenCommandFor(plan.member.Path, cachepolicy.Select(m.Data, plan.member.Path, plan.config.CacheSettings()), offline)
				if errors.Is(err, setup.ErrNoLockfile) {
					noCommand = true
					return saveProof(tx, b, plan, "")
				}
				if err != nil {
					return err
				}
			}
			if _, native := m.Backend.(execution.Host); native && !execution.Supported {
				return errors.New("workspace preparation commands require supervised execution on this host")
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = m.Admit(); err != nil {
				return err
			}
			if err = m.Quota(b); err != nil {
				return err
			}
			limit, err := m.ReserveLog(rows)
			if err != nil {
				return err
			}
			env, err = m.environment(ctx, plan)
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
			identity, err := m.Backend.Create(root, id)
			if err != nil {
				return err
			}
			created = true
			stage.HostID, stage.BootID = identity.HostID, identity.BootID
			b.Lease.Execution = stage
			b.Workspace.Stage = &model.WorkspaceStage{Name: plan.member.Name, Digest: plan.digest}
			continuation = b.Lease.Continuation
			return history.Start(tx, history.Record{ID: id, Key: key, Holder: b.Lease.Holder, StartedAt: time.Now().UTC()})
		})
	})
	if err != nil {
		if created {
			_ = m.Backend.Fence(stage)
		}
		return err
	}
	if noCommand {
		return nil
	}
	runner := stageexec.Runner{
		Backend: m.Backend,
		Start: func(ctx context.Context, cmd *exec.Cmd, e *execution.Execution) error {
			return m.start(ctx, key, token, continuation, plan, cmd, e)
		},
		Stop: func() (*execution.Execution, error) {
			b, err := m.stop(key, token, continuation, false, true)
			if err != nil {
				return nil, err
			}
			return b.Lease.Execution, nil
		},
		Cancel: func() { _, _ = m.Stop(key, token, false) },
		Seal: func(c *output.Capture, exit *int, truncated bool) error {
			return m.Seal(stage.ID, c, exit, truncated)
		},
	}
	if _, err = runner.Run(ctx, stageexec.Plan{Execution: stage, Dir: plan.member.Path, Args: args, Environment: env}); err != nil {
		return err
	}
	return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		b, err := owned(rows, key, token)
		if err != nil {
			return err
		}
		if err = initializing(b); err != nil {
			return err
		}
		if b.Lease.Continuation != continuation || b.Lease.Execution == nil || b.Lease.Execution.ID != stage.ID {
			return errors.New("workspace stage ownership changed before success save")
		}
		if err = m.stopped(b); err != nil {
			return err
		}
		if err = m.inspectMember(ctx, b, plan.member.Name); err != nil {
			return err
		}
		_, digest, err := inputs(plan.member.Path)
		if err != nil || digest != plan.digest {
			return errors.New("workspace preparation inputs changed during execution")
		}
		return saveProof(tx, b, plan, stage.ID)
	})
}

func (m Manager) start(ctx context.Context, key, token, continuation string, plan memberPlan, cmd *exec.Cmd, e *execution.Execution) error {
	return m.Gate(key, func() error {
		return m.Update(func(rows benchstate.Database, _ *sql.Tx) error {
			b, err := owned(rows, key, token)
			if err != nil {
				return err
			}
			if err = initializing(b); err != nil {
				return err
			}
			if b.Lease.Continuation != continuation || b.Lease.Execution == nil || b.Lease.Execution.ID != e.ID || b.Lease.Execution.Phase != "starting" {
				return errors.New("workspace stage was stopped or replaced")
			}
			if err = m.inspectMember(ctx, b, plan.member.Name); err != nil {
				return err
			}
			_, digest, err := inputs(plan.member.Path)
			if err != nil || digest != plan.digest {
				return errors.New("workspace preparation inputs changed before process start")
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = m.Backend.Start(cmd, e); err != nil {
				return err
			}
			b.Lease.Execution.Phase = "running"
			b.Lease.Execution.PID = cmd.Process.Pid
			return nil
		})
	})
}

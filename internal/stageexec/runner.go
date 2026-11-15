// Package stageexec runs an already-owned, durably recorded execution stage.
// The caller owns acquisition, atomic launch authority and stopped-state saves.
package stageexec

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
)

type Plan struct {
	Execution         *execution.Execution
	Dir               string
	Args, Environment []string
}

type Result struct {
	Execution    *execution.Execution
	ExitCode     int
	LogTruncated bool
}

type Runner struct {
	Backend interface {
		Fence(*execution.Execution) error
	}
	// Start must persist launch intent and authorize the actual process start
	// under the caller's launch/stop gate. An ambiguous failure retains ownership.
	Start func(context.Context, *exec.Cmd, *execution.Execution) error
	// Stop must fence descendants before durably recording a stopped execution.
	Stop func() (*execution.Execution, error)
	// Cancel reconciles explicit cancellation after the known scope is fenced,
	// even when the caller's state is currently unavailable.
	Cancel func()
	// Seal records the bounded capture only after Stop and capture.Finish succeed.
	Seal func(*output.Capture, *int, bool) error
}

func (r Runner) Run(ctx context.Context, plan Plan) (*Result, error) {
	if plan.Execution == nil || len(plan.Args) == 0 || r.Backend == nil || r.Start == nil || r.Stop == nil || r.Cancel == nil || r.Seal == nil {
		return nil, errors.New("execution stage requires a plan, fencing backend and lifecycle callbacks")
	}
	result := &Result{Execution: plan.Execution, ExitCode: -1}
	capture, err := output.Open(plan.Execution.Log, plan.Execution.LogLimitBytes)
	if err != nil {
		return result, err
	}
	defer capture.Close()
	cmd := exec.Command(plan.Args[0], plan.Args[1:]...)
	cmd.Dir = plan.Dir
	cmd.Stdout = capture.Writer
	cmd.Stderr = capture.Writer
	cmd.Env = plan.Environment
	err = r.Start(ctx, cmd, plan.Execution)
	_ = capture.Writer.Close()
	if err != nil {
		if cmd.Process != nil {
			if fenceErr := r.Backend.Fence(plan.Execution); fenceErr != nil {
				go func() { _ = cmd.Wait() }()
				return result, errors.Join(err, fmt.Errorf("execution fencing failed; lease retained: %w", fenceErr))
			}
			_ = cmd.Wait()
		}
		return result, fmt.Errorf("start failed; lease retained: %w", err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = r.Backend.Fence(plan.Execution)
			r.Cancel()
		case <-done:
		}
	}()
	waitErr := cmd.Wait()
	close(done)
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	stopped, err := r.Stop()
	if err != nil {
		return result, fmt.Errorf("unable to prove execution stopped; lease retained: %w", err)
	}
	result.Execution = stopped
	logErr := capture.Finish()
	result.LogTruncated = capture.Truncated
	if logErr != nil {
		return result, fmt.Errorf("command log failed; stopped lease retained: %w", logErr)
	}
	if err := r.Seal(capture, &result.ExitCode, result.LogTruncated); err != nil {
		return result, fmt.Errorf("log history save failed; stopped lease retained: %w", err)
	}
	if waitErr != nil {
		return result, fmt.Errorf("command failed; stopped lease retained: %w", waitErr)
	}
	return result, nil
}

// Package stageexec runs an already-owned, durably recorded execution stage.
// The caller owns acquisition, atomic launch authority and stopped-state saves.
package stageexec

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync/atomic"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
)

var ErrAmbiguousStart = errors.New("execution start callbacks are ambiguous")

const transportDrainTimeout = 5 * time.Second

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
	// Open optionally authorizes capture creation under the launch/stop gate.
	// A stopped pending runner must not create a file after absence approval.
	Open    func() (*output.Capture, error)
	Backend interface {
		Fence(*execution.Execution) error
	}
	// Start must persist launch intent and authorize the actual process start
	// under the caller's launch/stop gate. An ambiguous failure retains ownership.
	Start func(context.Context, *exec.Cmd, *execution.Execution) error
	// Prepare is required for installed service payload transport; it cannot be
	// combined with the legacy Start callback. StartPrepared must consume Launch
	// inside the same existing owner/state gate as backend.Start.
	Prepare       PrepareTransport
	StartPrepared func(context.Context, *exec.Cmd, *execution.Execution, Launch) error
	// Stop must fence descendants before durably recording a stopped execution.
	Stop func() (*execution.Execution, error)
	// Cancel reconciles explicit cancellation after the known scope is fenced,
	// even when the caller's state is currently unavailable.
	Cancel func()
	// Seal records the bounded capture only after Stop and capture.Finish succeed.
	Seal func(*output.Capture, *int, bool) error
}

func (r Runner) Run(ctx context.Context, plan Plan) (*Result, error) {
	if r.Start != nil && r.StartPrepared != nil {
		return nil, ErrAmbiguousStart
	}
	if plan.Execution == nil || len(plan.Args) == 0 || r.Backend == nil || (r.Start == nil && r.StartPrepared == nil) || r.Prepare != nil && r.StartPrepared == nil || r.Stop == nil || r.Cancel == nil || r.Seal == nil {
		return nil, errors.New("execution stage requires a plan, fencing backend and lifecycle callbacks")
	}
	result := &Result{Execution: plan.Execution, ExitCode: -1}
	open := r.Open
	if open == nil {
		open = func() (*output.Capture, error) { return output.Open(plan.Execution.Log, plan.Execution.LogLimitBytes) }
	}
	capture, err := open()
	if err != nil {
		return result, err
	}
	defer capture.Close()
	cmd := exec.Command(plan.Args[0], plan.Args[1:]...)
	cmd.Dir = plan.Dir
	cmd.Stdout = capture.Writer
	cmd.Stderr = capture.Writer
	cmd.Env = plan.Environment
	var transport Transport
	if r.Prepare != nil {
		transport, err = r.Prepare(cmd, plan.Execution)
		if err != nil {
			return result, err
		}
		if transport == nil {
			return result, errors.New("execution transport missing")
		}
		defer transport.Close()
	}
	wait := cmd.Wait
	if transport != nil {
		wait = transport.WaitLeader
	}
	var consumed atomic.Bool
	var launchErr error
	launch := func(start func() error) error {
		if start == nil || !consumed.CompareAndSwap(false, true) {
			launchErr = errors.New("execution launch must be consumed once")
			return launchErr
		}
		if transport != nil {
			launchErr = transport.Check()
			if launchErr != nil {
				return launchErr
			}
		}
		launchErr = start()
		if launchErr == nil && transport != nil {
			launchErr = transport.StartIO()
		}
		return launchErr
	}
	if r.StartPrepared != nil {
		err = r.StartPrepared(ctx, cmd, plan.Execution, launch)
		if err == nil && !consumed.Load() {
			err = errors.New("execution launch was not consumed")
		}
		err = errors.Join(err, launchErr)
	} else {
		err = r.Start(ctx, cmd, plan.Execution)
	}
	_ = capture.Writer.Close()
	if err != nil {
		if cmd.Process != nil {
			if fenceErr := r.Backend.Fence(plan.Execution); fenceErr != nil {
				go func() { _ = wait() }()
				return result, errors.Join(err, fmt.Errorf("execution fencing failed; lease retained: %w", fenceErr))
			}
			_ = wait()
		}
		return result, fmt.Errorf("start failed; lease retained: %w", err)
	}
	var transportErrors <-chan error
	if transport != nil {
		transportErrors = transport.Failures()
	}
	done := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		select {
		case <-ctx.Done():
			_ = r.Backend.Fence(plan.Execution)
			r.Cancel()
		case <-transportErrors:
			_ = r.Backend.Fence(plan.Execution)
			r.Cancel()
		case <-done:
		}
	}()
	waitErr := wait()
	close(done)
	// No cancellation callback may escape into a later operation that retains
	// the same lease token. Join before publishing the stopped/sealed result.
	<-monitorDone
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	stopped, err := r.Stop()
	if err != nil {
		return result, fmt.Errorf("unable to prove execution stopped; lease retained: %w", err)
	}
	result.Execution = stopped
	if transport != nil {
		// Cancellation has already fenced the run and joined its monitor. Finish
		// bounded cleanup independently so cancellation cannot randomly skip sealing.
		drainCtx, drainCancel := context.WithTimeout(context.WithoutCancel(ctx), transportDrainTimeout)
		drainErr := transport.Drain(drainCtx, r.Backend)
		drainCancel()
		if drainErr != nil {
			r.Cancel()
			return result, fmt.Errorf("command IO failed; stopped lease retained: %w", drainErr)
		}
	}
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

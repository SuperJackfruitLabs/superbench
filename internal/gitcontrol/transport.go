package gitcontrol

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// Transport owns command IO, never execution authority. The runner independently
// journals, starts and fences the real Host scope before accepting its drain.
type Transport interface {
	Check() error
	StartIO() error
	WaitLeader() error
	Failures() <-chan error
	Drain(context.Context, interface {
		Fence(*execution.Execution) error
	}) error
	Close() error
}

// Payload prepares only an explicit payload route. Ordinary trusted Git keeps
// using Run; choosing a payload must never fall back to that route on refusal.
type Payload interface {
	Prepare(*exec.Cmd, *execution.Execution) (Transport, error)
}

type payloadHost interface {
	Create(string, string) (execution.Identity, error)
	Start(*exec.Cmd, *execution.Execution) error
	Fence(*execution.Execution) error
}

// RunPayload keeps the same durable Git-control recovery and operation binding
// as Run. IO cannot start before durable intent and the atomic launch check.
func (r Runner) RunPayload(ctx context.Context, cmd *exec.Cmd, payload Payload) error {
	if ctx == nil || cmd == nil || payload == nil {
		return errors.New("Git payload command refused")
	}
	if err := validateRoot(r.Root); err != nil {
		return err
	}
	return r.runPayload(ctx, cmd, payload, execution.Host{})
}

// The production entry point supplies the real Host after root validation.
// The private core keeps IO/journal lifecycle tests independent of kernel grants.
func (r Runner) runPayload(ctx context.Context, cmd *exec.Cmd, payload Payload, host payloadHost) error {
	ctx, cancel, err := r.deadline(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if cmd == nil || payload == nil || ctx.Err() != nil {
		return errors.New("Git payload command refused")
	}
	dir, err := operationcontrol.Value(ctx, func() (string, error) { return controlDir(r.State, true) })
	if err != nil {
		return err
	}
	var idBytes [16]byte
	if _, err = rand.Read(idBytes[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(idBytes[:])
	identity, err := operationcontrol.Value(ctx, func() (execution.Identity, error) { return host.Create(r.Root, id) })
	if err != nil {
		return err
	}
	j := controlIntent{Version: controlVersion, Root: r.Root, ID: id, HostID: identity.HostID, BootID: identity.BootID}
	if op, ok := operationcontrol.FromContext(ctx); ok {
		j.OperationID = op.Target.ID()
		j.OperationEpoch = op.Epoch
	}
	scope := j.execution()
	if err = saveControl(dir, j); err != nil {
		return errors.Join(err, host.Fence(scope))
	}
	transport, err := payload.Prepare(cmd, scope)
	if err == nil && transport == nil {
		err = errors.New("Git payload transport missing")
	}
	if err != nil {
		if fenceErr := host.Fence(scope); fenceErr != nil {
			return errors.Join(err, fmt.Errorf("Git payload fence failed; intent retained: %w", fenceErr))
		}
		return errors.Join(err, removeControl(dir, id))
	}
	defer transport.Close()
	started := false
	err = operationcontrol.Effect(ctx, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := transport.Check(); err != nil {
			return err
		}
		if err := host.Start(cmd, scope); err != nil {
			return err
		}
		started = true
		return transport.StartIO()
	})
	if !started {
		if fenceErr := host.Fence(scope); fenceErr != nil {
			return errors.Join(err, fmt.Errorf("Git payload fence failed; intent retained: %w", fenceErr))
		}
		return errors.Join(err, removeControl(dir, id))
	}
	done := make(chan error, 1)
	go func() { done <- transport.WaitLeader() }()
	waitErr := err
	leaderDone := false
	if waitErr == nil {
		select {
		case waitErr = <-done:
			leaderDone = true
		case waitErr = <-transport.Failures():
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	}
	if fenceErr := host.Fence(scope); fenceErr != nil {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(2100 * time.Millisecond):
		}
		return errors.Join(waitErr, fmt.Errorf("Git payload fence failed; intent retained: %w", fenceErr))
	}
	// WaitLeader is idempotent; join it even if a pump/context notification won.
	if !leaderDone {
		select {
		case err = <-done:
			waitErr = errors.Join(waitErr, err)
		case <-time.After(2100 * time.Millisecond):
			return errors.Join(waitErr, errors.New("Git payload leader reap timed out; intent retained"))
		}
	}
	drainCtx, drainCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer drainCancel()
	if err = transport.Drain(drainCtx, host); err != nil {
		return errors.Join(waitErr, err)
	}
	// Cancellation/epoch admission is rechecked before a caller can publish
	// private output; quiescent control cleanup conveys no owner transfer.
	waitErr = errors.Join(waitErr, ctx.Err(), operationcontrol.Check(ctx))
	return errors.Join(waitErr, removeControl(dir, id))
}

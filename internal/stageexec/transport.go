package stageexec

import (
	"context"
	"os/exec"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
)

// Transport separates leader exit from descendant-held IO. Check and StartIO
// execute synchronously inside the caller's existing atomic launch gate.
// Drain must independently establish its actual pinned scope fence before
// returning success; a successful Stop alone is not an IO completion assertion.
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

// Launch is consumed exactly once, synchronously under the caller's launch
// gate, after current owner/execution checks. Its function performs the actual
// backend.Start. Launch validates transport authority before Start and delivers
// the helper frame only after Start succeeds, while the same gate remains held.
type Launch func(func() error) error

type PrepareTransport func(*exec.Cmd, *execution.Execution) (Transport, error)

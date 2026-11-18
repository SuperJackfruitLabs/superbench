//go:build linux

package main

import (
	"context"
	"io"
	"os"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshrecovery"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
)

// runRetiredInspection is the daemon's sudo route to root's read-only answer:
// whether root retired the allocation of the create whose canonical Spec is
// on stdin, a create that never completed (freshrecovery.InspectRetired).
// It takes the key and nothing else: no identity, path or policy override.
func runRetiredInspection(ctx context.Context, args []string, input *os.File, output io.Writer) error {
	if len(args) != 2 || len(args[1]) == 0 || len(args[1]) > 80 || input == nil {
		return errBootstrapCommand
	}
	raw, err := readBootstrapInputUntil(input, time.Now().Add(10*time.Second), payloadbootstrap.MaxSpec)
	if err != nil {
		return err
	}
	spec, err := payloadbootstrap.ParseSpec(raw)
	if err != nil {
		return err
	}
	if spec.Key != args[1] {
		return errBootstrapCommand
	}
	result, err := freshrecovery.InspectRetired(ctx, spec)
	if err != nil {
		return err
	}
	return operationOutput(output, result)
}

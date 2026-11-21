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
	spec, err := inspectionSpec(args, input)
	if err != nil {
		return err
	}
	result, err := freshrecovery.InspectRetired(ctx, spec)
	if err != nil {
		return err
	}
	return operationOutput(output, result)
}

// runAbsentInspection is the daemon's sudo route to root's read-only answer:
// whether root holds nothing at all of the create whose canonical Spec is on
// stdin, a create refused before its intent (freshrecovery.InspectAbsent).
// Like the retired inspection it takes the key and nothing else.
func runAbsentInspection(ctx context.Context, args []string, input *os.File, output io.Writer) error {
	spec, err := inspectionSpec(args, input)
	if err != nil {
		return err
	}
	result, err := freshrecovery.InspectAbsent(ctx, spec)
	if err != nil {
		return err
	}
	return operationOutput(output, result)
}

// inspectionSpec reads a read-only inspection's Spec for its key argument.
func inspectionSpec(args []string, input *os.File) (payloadbootstrap.Spec, error) {
	var zero payloadbootstrap.Spec
	if len(args) != 2 || len(args[1]) == 0 || len(args[1]) > 80 || input == nil {
		return zero, errBootstrapCommand
	}
	raw, err := readBootstrapInputUntil(input, time.Now().Add(10*time.Second), payloadbootstrap.MaxSpec)
	if err != nil {
		return zero, err
	}
	spec, err := payloadbootstrap.ParseSpec(raw)
	if err != nil {
		return zero, err
	}
	if spec.Key != args[1] {
		return zero, errBootstrapCommand
	}
	return spec, nil
}

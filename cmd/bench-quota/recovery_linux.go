//go:build linux

package main

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshrecovery"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationcontrol"
)

// runRecoveryCommand admits a Fresh recovery step only for a direct root caller
// without a policy override, and only after its complete input has been read.
func runRecoveryCommand(ctx context.Context, args []string, override bool, input *os.File, output io.Writer) error {
	if override || len(args) == 0 || !isRecoveryCommand(args[0]) || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" || input == nil {
		return errBootstrapCommand
	}
	key, identity, err := parseBootstrapCommand(args)
	if err != nil {
		return err
	}
	planning := strings.HasPrefix(args[0], "plan-")
	limit := payloadbootstrap.MaxSpec
	if !planning {
		limit = freshrecovery.MaxRequest
	}
	raw, err := readBootstrapInputUntil(input, time.Now().Add(10*time.Second), limit)
	if err != nil {
		return err
	}
	var spec payloadbootstrap.Spec
	var leaf freshrecovery.LeafPlan
	var repair freshrecovery.RepairPlan
	var planSHA string
	switch args[0] {
	case "remove-empty-bootstrap-leaf":
		spec, leaf, err = freshrecovery.ParseLeaf(raw)
	case "repair-bootstrap-quota":
		spec, repair, err = freshrecovery.ParseRepair(raw)
	case "clean-bootstrap":
		spec, planSHA, err = freshrecovery.ParseClean(raw)
	default:
		spec, err = payloadbootstrap.ParseSpec(raw)
	}
	if err != nil {
		return err
	}
	if spec.Key != key {
		return errBootstrapCommand
	}
	control, _, err := quotaoperationcontrol.Open(ctx)
	if err != nil {
		return err
	}
	manager, err := payloadbootstrap.Open(ctx, control, identity)
	if err != nil {
		return err
	}
	var result any
	switch args[0] {
	case "plan-remove-empty-bootstrap-leaf":
		result, err = freshrecovery.PlanLeaf(ctx, manager, spec)
	case "remove-empty-bootstrap-leaf":
		result, err = freshrecovery.RemoveLeaf(ctx, manager, spec, leaf)
	case "plan-repair-bootstrap-quota":
		result, err = freshrecovery.PlanRepair(ctx, manager, spec)
	case "repair-bootstrap-quota":
		result, err = freshrecovery.Repair(ctx, manager, spec, repair)
	case "plan-clean-bootstrap":
		result, err = freshrecovery.PlanClean(ctx, manager, spec)
	case "clean-bootstrap":
		result, err = freshrecovery.Clean(ctx, manager, spec, planSHA)
	}
	if err != nil {
		return err
	}
	return operationOutput(output, result)
}

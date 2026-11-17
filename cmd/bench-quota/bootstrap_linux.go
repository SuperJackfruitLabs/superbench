//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationcontrol"
)

var errBootstrapCommand = errors.New("invalid fresh payload bootstrap command")

func runBootstrapCommand(args []string, override bool, input *os.File, output io.Writer) error {
	if override || len(args) == 0 {
		return errBootstrapCommand
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if isRecoveryCommand(args[0]) {
		return runRecoveryCommand(ctx, args, override, input, output)
	}
	if args[0] == "initialize-indexed" {
		if len(args) != 1 || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" {
			return errBootstrapCommand
		}
		_, _, err := quotaoperationcontrol.InitializeFresh(ctx)
		return err
	}
	planning := args[0] == "plan-retire-bootstrap"
	retiring := args[0] == "retire-bootstrap"
	inspection := args[0] == "inspect-bootstrap-payload"
	if (inspection || planning || retiring) && (os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "") {
		return errBootstrapCommand
	}
	key, identity, err := parseBootstrapCommand(args)
	if err != nil {
		return err
	}
	if input == nil {
		return errBootstrapCommand
	}
	limit := payloadbootstrap.MaxSpec
	if retiring {
		limit = payloadbootstrap.MaxRetirement
	}
	raw, err := readBootstrapInputUntil(input, time.Now().Add(10*time.Second), limit)
	if err != nil {
		return err
	}
	var spec payloadbootstrap.Spec
	var plan payloadbootstrap.RetirementPlan
	if retiring {
		spec, plan, err = payloadbootstrap.ParseRetirement(raw)
	} else {
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
	if planning {
		result, err := manager.PlanRetirement(ctx, spec)
		if err != nil {
			return err
		}
		return operationOutput(output, result)
	}
	if retiring {
		result, err := manager.RetireAbsent(ctx, spec, plan)
		if err != nil {
			return err
		}
		return operationOutput(output, result)
	}
	if inspection {
		result, err := manager.Inspect(ctx, spec)
		if err != nil {
			return err
		}
		return operationOutput(output, result)
	}
	result, err := manager.Bootstrap(ctx, spec)
	if err != nil {
		return err
	}
	return operationOutput(output, result)
}

func parseBootstrapCommand(args []string) (string, quotaoperation.Identity, error) {
	var zero quotaoperation.Identity
	if len(args) != 7 || (args[0] != "bootstrap-payload" && args[0] != "inspect-bootstrap-payload" && args[0] != "plan-retire-bootstrap" && args[0] != "retire-bootstrap" && !isRecoveryCommand(args[0])) {
		return "", zero, errBootstrapCommand
	}
	i := quotaoperation.Identity{OperationID: args[2], Epoch: args[3], StateIdentity: args[4], Authority: args[5], BinarySHA256: args[6]}
	if !i.Valid() {
		return "", zero, errBootstrapCommand
	}
	// The complete canonical Spec parser checks this same key before any policy
	// read or authority admission. No command accepts paths or quota overrides.
	if len(args[1]) == 0 || len(args[1]) > 80 {
		return "", zero, errBootstrapCommand
	}
	for j, c := range args[1] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || j > 0 && (c == '_' || c == '-')) {
			return "", zero, errBootstrapCommand
		}
	}
	return args[1], i, nil
}

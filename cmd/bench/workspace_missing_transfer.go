package main

import (
	"context"
	"errors"
	"io"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

func workspaceMissingTransfer(ctx context.Context, store bench.Store, op, source, target, id, path, approval string, quota uint64, stdout io.Writer) error {
	if source == "" {
		return errors.New("source workspace key required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manager := workspacetransfer.Manager{Store: store}
	if op == "inspect-missing-transfer" {
		if target == "" || id == "" {
			return errors.New("missing transfer inspection requires --target and --transfer-id")
		}
		plan, err := manager.InspectMissing(ctx, source, target, id, quota)
		if err != nil {
			return err
		}
		raw, err := plan.Bytes()
		if err != nil {
			return err
		}
		if len(raw) > workspacetransfer.MaxPlan {
			return errors.New("missing workspace transfer plan exceeds output limit")
		}
		_, err = stdout.Write(raw)
		return err
	}
	var result *workspacetransfer.Result
	var err error
	if op == "begin-missing-transfer" {
		if path == "" || approval == "" {
			return errors.New("missing workspace transfer requires --plan and --approve matching the reviewed Digest")
		}
		raw, e := workspaceReadFile(path, workspacetransfer.MaxPlan)
		if e != nil {
			return e
		}
		plan, e := workspacetransfer.ParseMissingPlan(raw)
		if e != nil {
			return e
		}
		if plan.SourceKey != source || plan.Digest != approval {
			return errors.New("approval must match the exact missing workspace transfer plan and source key")
		}
		result, err = manager.BeginMissing(ctx, plan)
	} else {
		if id == "" {
			return errors.New("missing workspace transfer continuation requires --transfer-id")
		}
		result, err = manager.ContinueMissing(ctx, source, id)
	}
	if result != nil {
		if outputErr := writeWorkspaceTransferResult(stdout, result); outputErr != nil {
			return errors.Join(err, outputErr)
		}
	}
	return err
}

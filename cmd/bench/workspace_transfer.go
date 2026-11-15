package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

func workspaceTransfer(ctx context.Context, store bench.Store, op, source, target, id, path, approval string, quota uint64, stdout io.Writer) error {
	if source == "" {
		return errors.New("source workspace key required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manager := workspacetransfer.Manager{Store: store}
	if op == "inspect-transfer" {
		if target == "" || id == "" {
			return errors.New("transfer inspection requires --target and --transfer-id")
		}
		plan, err := manager.Inspect(ctx, source, target, id, quota)
		if err != nil {
			return err
		}
		raw, err := plan.Bytes()
		if err != nil {
			return err
		}
		if len(raw) > workspacetransfer.MaxPlan {
			return errors.New("workspace transfer plan exceeds output limit")
		}
		_, err = stdout.Write(raw)
		return err
	}
	var result *workspacetransfer.Result
	var err error
	if op == "begin-transfer" {
		if path == "" || approval == "" {
			return errors.New("workspace transfer requires --plan and --approve matching the reviewed Digest")
		}
		raw, readErr := workspaceReadFile(path, workspacetransfer.MaxPlan)
		if readErr != nil {
			return readErr
		}
		plan, parseErr := workspacetransfer.ParsePlan(raw)
		if parseErr != nil {
			return parseErr
		}
		if plan.SourceKey != source || plan.Digest != approval {
			return errors.New("approval must match the exact workspace transfer plan and source key")
		}
		result, err = manager.Begin(ctx, plan)
	} else {
		if id == "" {
			return errors.New("workspace transfer continuation requires --transfer-id")
		}
		// The manager checks this source against the stored plan before effects.
		result, err = manager.Continue(ctx, source, id)
	}
	if result != nil {
		if outputErr := writeWorkspaceTransferResult(stdout, result); outputErr != nil {
			return errors.Join(err, outputErr)
		}
	}
	return err
}

func writeWorkspaceTransferResult(stdout io.Writer, result *workspacetransfer.Result) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(raw)+1 > workspacetransfer.MaxResult {
		return errors.New("workspace transfer result exceeds output limit; inspect retained state")
	}
	_, err = stdout.Write(append(raw, '\n'))
	return err
}

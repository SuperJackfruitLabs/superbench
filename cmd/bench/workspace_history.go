package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
)

// Interrupted history reconciliation records reviewed unknown outcomes only.
// It never resumes a command, creates setup success or exports a lease token.
func workspaceHistory(ctx context.Context, store bench.Store, op, key, path, approval string, stdout io.Writer) error {
	if key == "" {
		return errors.New("workspace key required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.WithWorkspace(key, false, nil); err != nil {
		return err
	}
	if op == "inspect-history" {
		plan, err := store.InspectWorkspaceHistory(key)
		if err != nil {
			return err
		}
		raw, err := plan.Bytes()
		if err != nil {
			return err
		}
		if len(raw) > workspaceprep.MaxHistoryPlan {
			return errors.New("workspace history plan exceeds output limit")
		}
		_, err = stdout.Write(raw)
		return err
	}
	if path == "" || approval == "" {
		return errors.New("workspace history reconciliation requires --plan and --approve matching the reviewed Digest")
	}
	raw, err := workspaceReadFile(path, workspaceprep.MaxHistoryPlan)
	if err != nil {
		return err
	}
	plan, err := workspaceprep.ParseHistoryPlan(raw)
	if err != nil {
		return err
	}
	if plan.Key != key || plan.Digest != approval {
		return errors.New("approval must match the exact workspace history plan and key")
	}
	record, err := store.ReconcileWorkspaceHistory(plan)
	if err != nil {
		return err
	}
	raw, err = json.Marshal(record)
	if err != nil {
		return err
	}
	if len(raw)+1 > workspaceprep.MaxHistoryPlan {
		return errors.New("workspace history record exceeds output limit")
	}
	_, err = stdout.Write(append(raw, '\n'))
	return err
}

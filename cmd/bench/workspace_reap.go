package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceapi"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacearchive"
)

func workspaceReadFile(path string, limit int) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, errors.New("workspace input must be a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, errors.New("workspace input exceeds limit")
	}
	return raw, nil
}

func workspaceReap(ctx context.Context, store bench.Store, op, key, path, approval string, stdout io.Writer) error {
	if key == "" {
		return errors.New("workspace key required")
	}
	if err := store.WithWorkspace(key, false, nil); err != nil {
		return err
	}
	if op == "plan-reap" {
		if path != "" || approval != "" {
			return errors.New("plan-reap does not accept deletion approval")
		}
		plan, err := store.PlanWorkspaceReapContext(ctx, key)
		if err != nil {
			return err
		}
		return writeWorkspacePlan(stdout, plan)
	}
	if path == "" || approval == "" {
		return errors.New("workspace reap requires --plan and --approve matching the reviewed Digest")
	}
	raw, err := workspaceReadFile(path, reap.MaxManifest)
	if err != nil {
		return err
	}
	plan, err := workspacearchive.ReadPlan(raw)
	if err != nil {
		return err
	}
	if plan.Key != key || plan.Digest != approval {
		return errors.New("approval must match the exact aggregate plan and key")
	}
	b, err := store.ReapWorkspaceContext(ctx, *plan)
	if b != nil {
		if writeErr := json.NewEncoder(stdout).Encode(workspaceapi.Inventory(b)); writeErr != nil {
			return writeErr
		}
	}
	return err
}

func writeWorkspacePlan(stdout io.Writer, plan *workspacearchive.Plan) error {
	raw, err := plan.Bytes()
	if err != nil {
		return err
	}
	_, err = stdout.Write(raw)
	return err
}

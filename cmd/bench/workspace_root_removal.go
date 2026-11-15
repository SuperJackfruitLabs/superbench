package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacearchive"
)

var rootRemovalName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

func workspaceRootRemoval(ctx context.Context, store bench.Store, op, key, id, path, approval string, stdout io.Writer) error {
	if !rootRemovalName.MatchString(key) {
		return errors.New("valid workspace key required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if op == "inspect-root-removal" && !rootRemovalName.MatchString(id) {
		return errors.New("root removal inspection requires a fresh --disposition-id")
	}
	var reviewed *workspacearchive.RootRemovalReview
	if op == "reconcile-root-removal" {
		if path == "" || approval == "" {
			return errors.New("current empty-directory removal requires --plan and --approve matching its reviewed Digest")
		}
		raw, err := workspaceReadFile(path, workspacearchive.MaxRootRemovalReview)
		if err != nil {
			return err
		}
		reviewed, err = workspacearchive.ParseRootRemovalReview(raw)
		if err != nil {
			return err
		}
		if reviewed.Key != key || reviewed.Digest != approval || reviewed.State != store.State || reviewed.DataRoot != store.Data {
			return errors.New("approval must match the exact current empty-directory removal plan, key and storage")
		}
	} else if op != "inspect-root-removal" {
		return errors.New("unknown root removal operation")
	}
	manager, ctx, err := store.WorkspaceArchiveAdministration(ctx)
	if err != nil {
		return err
	}
	if op == "inspect-root-removal" {
		plan, err := manager.InspectRootRemoval(ctx, key, id)
		if err != nil {
			return err
		}
		raw, err := plan.Bytes()
		if err != nil {
			return err
		}
		_, err = stdout.Write(append(raw, '\n'))
		return err
	}
	if err := manager.ReconcileRootRemoval(ctx, *reviewed); err != nil {
		return err
	}
	// This closed acknowledgement grants no lease and does not claim that quota
	// retirement or reap has completed. Resume the original reviewed reap next.
	raw, err := json.Marshal(struct {
		Key   string `json:"key"`
		ID    string `json:"disposition_id"`
		Phase string `json:"phase"`
	}{key, reviewed.ID, "root-absent"})
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(raw, '\n'))
	return err
}

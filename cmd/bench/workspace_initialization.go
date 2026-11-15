package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
)

var repairMemberName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func initializationRerunMembers(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	members := strings.Split(value, ",")
	if len(members) > 32 {
		return nil, errors.New("too many initialization repair members")
	}
	sort.Strings(members)
	for i, name := range members {
		if !repairMemberName.MatchString(name) || i > 0 && members[i-1] == name {
			return nil, errors.New("initialization repair requires distinct registered member names")
		}
	}
	return members, nil
}

func workspaceInitialization(ctx context.Context, store bench.Store, op, key, path, approval, repairID, token, rerun string, stdout io.Writer) error {
	if key == "" {
		return errors.New("workspace key required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	members, err := initializationRerunMembers(rerun)
	if err != nil {
		return err
	}
	var plan workspacerepair.Plan
	if op == "claim-initialization" {
		if path == "" || approval == "" || repairID == "" {
			return errors.New("initialization repair claim requires --plan, --approve and --repair-id")
		}
		raw, err := workspaceReadFile(path, workspacerepair.MaxPlan)
		if err != nil {
			return err
		}
		plan, err = workspacerepair.ParsePlan(raw)
		if err != nil {
			return err
		}
		if plan.Key != key || plan.Digest != approval {
			return errors.New("approval must match the exact initialization repair plan and key")
		}
	}
	if op == "continue-initialization" && (repairID == "" || token == "") {
		return errors.New("initialization repair continuation requires --repair-id and --token")
	}
	manager, err := store.WorkspaceRepair()
	if err != nil {
		return err
	}
	if op == "inspect-initialization" {
		plan, err := manager.Inspect(ctx, key, members)
		if err != nil {
			return err
		}
		raw, err := plan.Bytes()
		if err != nil {
			return err
		}
		if len(raw) > workspacerepair.MaxPlan {
			return errors.New("initialization repair plan exceeds output limit")
		}
		_, err = stdout.Write(raw)
		return err
	}
	var result *workspacerepair.Result
	if op == "claim-initialization" {
		result, err = manager.Claim(ctx, plan, repairID)
	} else {
		result, err = manager.Continue(ctx, key, repairID, token)
	}
	if result != nil {
		if outputErr := writeInitializationResult(stdout, result); outputErr != nil {
			return errors.Join(err, outputErr)
		}
	}
	return err
}

// Only the backend's closed local result may be serialized. Private workspace
// rows and their continuations never pass through this output path.
func writeInitializationResult(stdout io.Writer, result *workspacerepair.Result) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(raw)+1 > workspacerepair.MaxResult {
		return errors.New("initialization repair result exceeds output limit; inspect retained state")
	}
	_, err = stdout.Write(append(raw, '\n'))
	return err
}

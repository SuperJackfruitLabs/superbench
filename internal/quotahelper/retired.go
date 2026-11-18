package quotahelper

import (
	"context"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// Retired asks the fixed helper's read-only inspect-retired-payload step
// whether root retired the allocation of the daemon's create that never
// completed, whose canonical bootstrap Spec is spec, and returns its checked
// tombstone. It reserves no operation and has no effect; the daemon records
// nothing on its own word, only on this reply.
func Retired(ctx context.Context, c Config, key, id string, generation uint32, spec []byte) (RetiredResult, error) {
	return retiredScoped(ctx, c, key, id, generation, spec, os.Geteuid(), 0)
}

func retiredScoped(ctx context.Context, c Config, key, id string, generation uint32, spec []byte, uid int, trustedUID uint32) (RetiredResult, error) {
	var zero RetiredResult
	if !reclaimKey.MatchString(key) || !workspaceID.MatchString(id) || len(spec) == 0 || len(spec) > MaxReply || c.PolicyOverride != "" {
		return zero, operationcontrol.ErrAuthority
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	input := append([]byte(nil), spec...)
	binary, err := installedBinary(c.Command, trustedUID)
	if err != nil {
		return zero, err
	}
	raw, err := helperCommand(ctx, c, uid, false, nil, "operation-info")
	if err != nil {
		return zero, err
	}
	var info Info
	if err = strictReply(raw, &info); err != nil {
		return zero, err
	}
	if !validInfo(info) || info.Root != c.Data || info.BinarySHA256 != binary {
		return zero, operationcontrol.ErrAuthority
	}
	recheck := func() error {
		actual, err := installedBinary(c.Command, trustedUID)
		if err != nil {
			return err
		}
		if actual != binary {
			return operationcontrol.ErrAuthority
		}
		return nil
	}
	raw, err = helperCommandInput(ctx, c, uid, false, recheck, input, "inspect-retired-payload", key)
	if err != nil {
		return zero, err
	}
	return ParseRetiredResult(raw, c.Data, key, id, generation)
}

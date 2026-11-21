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
	raw, err := inspectScoped(ctx, c, "inspect-retired-payload", key, id, spec, uid, trustedUID)
	if err != nil {
		return RetiredResult{}, err
	}
	return ParseRetiredResult(raw, c.Data, key, id, generation)
}

// Absent asks the fixed helper's read-only inspect-absent-payload step
// whether root holds nothing of the daemon's create that never completed,
// whose canonical bootstrap Spec is spec: no intent, no allocation of its
// own and no leaf. It reserves no operation and has no effect.
func Absent(ctx context.Context, c Config, key, id string, generation uint32, spec []byte) (AbsentResult, error) {
	return absentScoped(ctx, c, key, id, generation, spec, os.Geteuid(), 0)
}

func absentScoped(ctx context.Context, c Config, key, id string, generation uint32, spec []byte, uid int, trustedUID uint32) (AbsentResult, error) {
	raw, err := inspectScoped(ctx, c, "inspect-absent-payload", key, id, spec, uid, trustedUID)
	if err != nil {
		return AbsentResult{}, err
	}
	return ParseAbsentResult(raw, key, id, generation)
}

// inspectScoped runs one read-only inspection with only the key as argument
// and the Spec on stdin, after checking the installed helper's identity.
func inspectScoped(ctx context.Context, c Config, command, key, id string, spec []byte, uid int, trustedUID uint32) ([]byte, error) {
	var zero []byte
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
	return helperCommandInput(ctx, c, uid, false, recheck, input, command, key)
}

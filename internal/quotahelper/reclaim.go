package quotahelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

var workspaceID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Reclaim sends a released Fresh workspace's canonical bootstrap Spec to the
// fixed helper's reclaim-payload step and returns its checked tombstone. The
// daemon must already have recorded the workspace as reclaiming: a reclaim is
// one-way. Its operation identity derives from the workspace alone (key and
// ID) with the helper's current authority and binary, so every retry after a
// crash or a lost reply names the same reservation. It is never a create's
// identity, it is not a receipt operation, and it has no cancellation route:
// a reclaim is only ever finished.
func Reclaim(ctx context.Context, c Config, key, id string, project uint32, spec []byte) (ReclaimResult, error) {
	return reclaimScoped(ctx, c, key, id, project, spec, os.Geteuid(), 0)
}

// ReclaimIdentity is the reclaim operation's identity for one workspace.
func ReclaimIdentity(key, id, authority, binary, state string) quotaoperation.Identity {
	digest := func(domain string) string {
		raw, _ := json.Marshal(struct{ Domain, Key, ID string }{domain, key, id})
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	return quotaoperation.Identity{OperationID: digest("superbench.reclaim.v1"), Epoch: digest("superbench.reclaim-epoch.v1")[:32], StateIdentity: state, Authority: authority, BinarySHA256: binary}
}

func reclaimScoped(ctx context.Context, c Config, key, id string, project uint32, spec []byte, uid int, trustedUID uint32) (ReclaimResult, error) {
	var zero ReclaimResult
	if _, scoped := operationcontrol.FromContext(ctx); scoped || !reclaimKey.MatchString(key) || !workspaceID.MatchString(id) || project < 10000 || len(spec) == 0 || len(spec) > MaxReply || c.PolicyOverride != "" {
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
	state, device, inode, err := stateIdentity(c.State)
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
	h := operationcontrol.Helper{Command: c.Command, Policy: "/etc/superbench/storage.json", State: state, StateDevice: device, StateInode: inode, Kind: "bench", Key: key}
	i := ReclaimIdentity(key, id, info.Authority, binary, h.StateIdentity())
	recheck := func() error {
		named, dev, ino, err := stateIdentity(c.State)
		if err != nil {
			return err
		}
		actual, err := installedBinary(c.Command, trustedUID)
		if err != nil {
			return err
		}
		if named != state || dev != device || ino != inode || actual != binary {
			return operationcontrol.ErrAuthority
		}
		return nil
	}
	raw, err = helperCommandInput(ctx, c, uid, false, recheck, input, "reclaim-payload", key, i.OperationID, i.Epoch, i.StateIdentity, i.Authority, i.BinarySHA256)
	if err != nil {
		return zero, err
	}
	return ParseReclaimResult(raw, c.Data, key, project)
}

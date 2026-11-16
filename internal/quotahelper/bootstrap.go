package quotahelper

import (
	"context"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

// Bootstrap sends a finite canonical bootstrap specification to the fixed
// helper. It has no unscoped or ordinary-Apply fallback. The helper owns complete
// canonical Spec validation before any effects. Callers must validate its layout
// reply against the installed service authority and original full member vector.
// A private fresh-workspace adapter can select this transport behind its
// default-off internal gate; there is no public activation setting.
func Bootstrap(ctx context.Context, c Config, key string, requested uint64, spec []byte) ([]byte, error) {
	return bootstrapScoped(ctx, c, key, requested, spec, os.Geteuid(), 0)
}

func bootstrapScoped(ctx context.Context, c Config, key string, requested uint64, spec []byte, uid int, trustedUID uint32) ([]byte, error) {
	i, ok := operationcontrol.FromContext(ctx)
	if !ok || i.Key != key || (i.Operation != "create" && i.Operation != "recover") || len(spec) == 0 || len(spec) > MaxReply || c.PolicyOverride != "" {
		return nil, operationcontrol.ErrAuthority
	}
	if err := operationcontrol.Check(ctx); err != nil {
		return nil, err
	}
	// Copy before any wait: the exact bytes dispatched cannot change after helper
	// intent admission because a caller mutates its original slice.
	input := append([]byte(nil), spec...)
	binary, err := installedBinary(c.Command, trustedUID)
	if err != nil {
		return nil, err
	}
	state, device, inode, err := stateIdentity(c.State)
	if err != nil {
		return nil, err
	}
	raw, err := helperCommand(ctx, c, uid, true, nil, "operation-info")
	if err != nil {
		return nil, err
	}
	var info Info
	if err = strictReply(raw, &info); err != nil {
		return nil, err
	}
	if !validInfo(info) || info.Root != c.Data || info.BinarySHA256 != binary {
		return nil, operationcontrol.ErrAuthority
	}
	h := operationcontrol.Helper{Identity: quotaoperation.Identity{OperationID: i.Target.ID(), Epoch: i.Epoch, Authority: info.Authority, BinarySHA256: binary}, Command: c.Command, Policy: "/etc/superbench/storage.json", State: state, StateDevice: device, StateInode: inode, Kind: "bench", Key: key, Requested: requested}
	h.Identity.StateIdentity = h.StateIdentity()
	if err = operationcontrol.RecordHelper(ctx, h); err != nil {
		return nil, err
	}
	recheck := func() error {
		named, dev, ino, err := stateIdentity(c.State)
		if err != nil {
			return err
		}
		actual, err := installedBinary(c.Command, trustedUID)
		if err != nil {
			return err
		}
		if named != h.State || dev != h.StateDevice || ino != h.StateInode || actual != binary {
			return operationcontrol.ErrAuthority
		}
		return nil
	}
	return helperCommandInput(ctx, c, uid, true, recheck, input, "bootstrap-payload", key, h.Identity.OperationID, h.Identity.Epoch, h.Identity.StateIdentity, h.Identity.Authority, h.Identity.BinarySHA256)
}

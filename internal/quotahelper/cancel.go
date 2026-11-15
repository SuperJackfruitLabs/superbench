package quotahelper

import (
	"context"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

// Cancel invokes the original fixed helper's durable deny barrier. It deliberately
// does not use the now-closed operation launch gate, and never holds a receipt
// mutex over a privileged helper wait. The proof covers only this helper identity;
// execution/history fencing remains an independent terminal-cancellation gate.
func Cancel(ctx context.Context, i operationcontrol.Intent, h operationcontrol.Helper) (quotaoperation.Proof, error) {
	return cancel(ctx, i, h, os.Geteuid(), 0)
}
func cancel(ctx context.Context, i operationcontrol.Intent, h operationcontrol.Helper, uid int, trustedUID uint32) (quotaoperation.Proof, error) {
	var zero quotaoperation.Proof
	if !h.Valid(i) {
		return zero, operationcontrol.ErrAuthority
	}
	check := func() error {
		binary, err := installedBinary(h.Command, trustedUID)
		if err != nil {
			return err
		}
		state, dev, ino, err := stateIdentity(h.State)
		if err != nil {
			return err
		}
		if binary != h.Identity.BinarySHA256 || state != h.State || dev != h.StateDevice || ino != h.StateInode {
			return operationcontrol.ErrAuthority
		}
		return nil
	}
	if err := check(); err != nil {
		return zero, err
	}
	c := Config{State: h.State, Command: h.Command}
	if h.Policy != "/etc/superbench/storage.json" {
		c.PolicyOverride = h.Policy
	}
	raw, err := helperCommand(ctx, c, uid, false, check, "cancel-operation", h.Identity.OperationID, h.Identity.Epoch, h.Identity.StateIdentity, h.Identity.Authority, h.Identity.BinarySHA256)
	if err != nil {
		return zero, err
	}
	var proof quotaoperation.Proof
	if err = strictReply(raw, &proof); err != nil {
		return zero, err
	}
	if proof.Version != quotaoperation.Version || proof.Identity != h.Identity || !proof.Cancelled {
		return zero, operationcontrol.ErrAuthority
	}
	return proof, nil
}

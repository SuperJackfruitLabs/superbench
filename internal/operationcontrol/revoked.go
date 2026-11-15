package operationcontrol

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"

// RequireRevoked recognizes only the original operation's stopped capability.
// Clearing continuation alone cannot replace its BID, holder or lease token.
// It never authorizes a launch or adopts a later owner's lease.
func RequireRevoked(r Reader, i Intent, b *model.Bench) error {
	found, err := Get(r, i.Target)
	if err != nil {
		return err
	}
	if found.Intent != i || found.Phase != "bound" || b == nil || b.Workspace == nil || b.Lease == nil || b.Key != i.Key || b.Workspace.ID != found.BenchID || b.Lease.Holder != found.Holder || hash(b.Lease.Token) != found.TokenHash || !b.Lease.StopRequested || b.Lease.Continuation != "" {
		return ErrAuthority
	}
	if b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped" {
		return ErrAuthority
	}
	return nil
}

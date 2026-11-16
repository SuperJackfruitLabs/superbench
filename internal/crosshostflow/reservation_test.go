package crosshostflow

import (
	"context"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacetransfer"
)

func TestLocalTransferAndCrossHostOfferCannotReserveSameTarget(t *testing.T) {
	for _, offerFirst := range []bool{true, false} {
		name := "local-first"
		if offerFirst {
			name = "offer-first"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			local := workspacetransfer.Manager{Store: f.source}
			cross := Manager{Store: f.source}
			p, err := local.Inspect(ctx, f.spec.Key, "reserved_target", "local-transfer", 1<<20)
			must(t, err)
			if offerFirst {
				_, err = cross.Offer(ctx, "reserved_target", "incoming-create", 1<<20)
				must(t, err)
				if _, err = local.Begin(ctx, *p); err == nil {
					t.Fatal("local transfer excluded source despite retained cross-host reservation")
				}
				if snapshot(t, f.source, f.spec.Key).Workspace.Transfer != nil {
					t.Fatal("failed local reservation changed source")
				}
				b, err := f.source.AcquireWorkspaceContext(ctx, f.spec.Key, "still-available")
				must(t, err)
				if b.Lease == nil {
					t.Fatal("source lost availability")
				}
			} else {
				_, err = local.Begin(ctx, *p)
				must(t, err)
				if _, err = cross.Offer(ctx, "reserved_target", "incoming-create", 1<<20); err == nil {
					t.Fatal("cross-host offer stole local transfer target")
				}
				if _, err = local.Begin(ctx, *p); err != nil {
					t.Fatal("failed offer damaged original transfer", err)
				}
			}
		})
	}
}

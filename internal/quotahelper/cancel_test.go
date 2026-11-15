package quotahelper

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func TestHelperCancellationBypassesClosedLaunchGateButPinsOriginalInvocation(t *testing.T) {
	ctx, c, recorded, record := scopedFixture(t)
	if _, err := applyScoped(ctx, c, "bench", "card", 4096, 0, uint32(os.Geteuid())); err != nil {
		t.Fatal(err)
	}
	i, _ := operationcontrol.FromContext(ctx)
	h := (*recorded)[0]
	// Keep the actual executable identical to the admitted image; its controlled
	// fixture case below consumes the independent proof supplied by this test.
	proof := quotaoperation.Proof{Version: quotaoperation.Version, Identity: h.Identity, Cancelled: true}
	raw, _ := json.Marshal(proof)
	t.Setenv("QUOTA_SCOPED_CANCEL", string(raw))
	blocked, _ := operationcontrol.WithScope(context.Background(), i, func(func() error) error { return context.Canceled })
	if _, err := cancel(blocked, i, h, 0, uint32(os.Geteuid())); err != nil {
		t.Fatal(err)
	}
	commands, _ := os.ReadFile(record)
	if string(commands) != "operation-info\napply-operation\ncancel-operation\n" {
		t.Fatal(string(commands))
	}
	for _, fault := range []string{"proof", "state", "binary"} {
		t.Run(fault, func(t *testing.T) {
			wrong := h
			switch fault {
			case "proof":
				t.Setenv("QUOTA_SCOPED_CANCEL", `{"Version":"superbench.quota-operation.v1","Cancelled":true}`)
			case "state":
				wrong.StateInode++
				wrong.Identity.StateIdentity = wrong.StateIdentity()
			case "binary":
				wrong.Identity.BinarySHA256 = strings.Repeat("e", 64)
			}
			if _, err := cancel(blocked, i, wrong, 0, uint32(os.Geteuid())); err == nil {
				t.Fatal("changed authority certified")
			}
		})
	}
}

package transferguard_test

import (
	"context"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/transferguard"
)

func TestIncomingAuthorityIsSeparateFromInitializationToken(t *testing.T) {
	for _, phase := range []string{"materializing", "initializing", "prepared"} {
		b := &model.Bench{Lease: &model.Lease{Token: "known-token"}, Workspace: &model.Workspace{Phase: phase, TransferredFrom: &model.WorkspaceTransferOrigin{ID: "incoming"}}}
		if transferguard.Authority(b, "", "") == nil || transferguard.Authority(b, "different", "") == nil {
			t.Fatal("token adopted incoming continuation", phase)
		}
		if err := transferguard.Authority(b, "incoming", ""); err != nil {
			t.Fatal(err)
		}
		b.Workspace.InitializationRepair = &model.WorkspaceInitializationRepair{ID: "repair"}
		if transferguard.Authority(b, "incoming", "") == nil || transferguard.Authority(b, "incoming", "different") == nil {
			t.Fatal("transfer authority adopted repair continuation", phase)
		}
		if err := transferguard.Authority(b, "", "repair"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadyTargetHasNoPerpetualSourceDependency(t *testing.T) {
	b := &model.Bench{Workspace: &model.Workspace{Phase: "ready", TransferredFrom: &model.WorkspaceTransferOrigin{ID: "historical"}, InitializationRepair: &model.WorkspaceInitializationRepair{ID: "completed"}}}
	if err := transferguard.Authority(b, "", ""); err != nil {
		t.Fatal(err)
	}
	// No database, Git, source checkout or source storage is consulted after the
	// complete ready transition. The completed provenance remains metadata.
	if err := transferguard.Check(context.Background(), transferguard.Config{}, nil, b); err != nil {
		t.Fatal(err)
	}
}

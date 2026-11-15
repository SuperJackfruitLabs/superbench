package workspaceplan

import (
	"context"
	"strings"
	"testing"
)

func TestOrdinaryPlannerCannotSelectJournalTransfer(t *testing.T) {
	spec := Spec{TransferID: "approved-transfer", Key: "target", CheckpointRepository: "api", Repositories: []Repository{{Name: "api", Repository: "/srv/source.git"}}}
	normalized, err := Normalize(spec)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.TransferID != spec.TransferID || normalized.Repositories[0].Branch != "bench/target" || normalized.Repositories[0].Base != "main" {
		t.Fatal("normalization discarded staging identity or defaults")
	}
	_, err = (Planner{}).Select(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "approved journal staging") {
		t.Fatal("ordinary selection did not refuse transfer before managed source effects", err)
	}
}

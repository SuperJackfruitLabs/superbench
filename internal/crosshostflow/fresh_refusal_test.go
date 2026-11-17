package crosshostflow

import (
	"context"
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree/freshtreetest"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
)

func TestRegisteredFreshWorkspaceRefusesCrossHostExport(t *testing.T) {
	state := t.TempDir()
	b := freshtreetest.Bench(t.TempDir())
	freshtreetest.Register(t, state, b)
	a := workspacerepair.Manager{Lifecycle: workspace.Lifecycle{State: state}}
	if _, err := inspect(context.Background(), a, benchstate.Database{b.Key: b}, nil, Plan{SourceKey: b.Key}); !errors.Is(err, freshtree.ErrUnavailable) {
		t.Fatalf("cross-host export accepted a Fresh workspace: %v", err)
	}
}

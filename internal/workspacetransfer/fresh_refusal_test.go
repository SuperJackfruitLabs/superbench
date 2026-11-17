package workspacetransfer

import (
	"context"
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree/freshtreetest"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
)

func TestRegisteredFreshWorkspaceRefusesTransfer(t *testing.T) {
	state := t.TempDir()
	b := freshtreetest.Bench(t.TempDir())
	freshtreetest.Register(t, state, b)
	m := Manager{Store: bench.Store{State: state}}
	rows := benchstate.Database{b.Key: b}
	if _, err := m.inspectState(context.Background(), workspacerepair.Manager{}, rows, Plan{SourceKey: b.Key}); !errors.Is(err, freshtree.ErrUnavailable) {
		t.Fatalf("transfer accepted a Fresh workspace: %v", err)
	}
	if _, err := m.inspectMissingState(context.Background(), workspacerepair.Manager{}, rows, nil, MissingPlan{SourceKey: b.Key}, nil); !errors.Is(err, freshtree.ErrUnavailable) {
		t.Fatalf("missing-source transfer accepted a Fresh workspace: %v", err)
	}
}

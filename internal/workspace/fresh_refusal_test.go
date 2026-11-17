package workspace_test

import (
	"context"
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree/freshtreetest"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func TestRegisteredFreshWorkspaceRefusesTransferredInspection(t *testing.T) {
	state, data := t.TempDir(), t.TempDir()
	b := freshtreetest.Bench(data)
	freshtreetest.Register(t, state, b)
	l := workspace.Lifecycle{State: state, Data: data, Git: func(context.Context, string, ...string) (string, error) { return "", nil }}
	if _, err := l.InspectTransferred(context.Background(), b, false); !errors.Is(err, freshtree.ErrUnavailable) {
		t.Fatalf("transferred inspection accepted a Fresh workspace: %v", err)
	}
	// An installed helper route is refused too: a Fresh tree is never transferred.
	l.Fresh = func(*model.Bench) (freshtree.Tree, error) { return route{}, nil }
	if _, err := l.InspectTransferred(context.Background(), b, false); !errors.Is(err, freshtree.ErrUnavailable) {
		t.Fatalf("transferred inspection used a Fresh route: %v", err)
	}
}

type route struct{ freshtree.Tree }

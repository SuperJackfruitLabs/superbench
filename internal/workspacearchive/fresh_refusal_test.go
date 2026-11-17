package workspacearchive

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree/freshtreetest"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func TestRegisteredFreshWorkspaceRefusesArchiveAndMergeObservation(t *testing.T) {
	state := t.TempDir()
	b := freshtreetest.Bench(t.TempDir())
	freshtreetest.Register(t, state, b)
	if err := (Manager{State: state}).available(b); !errors.Is(err, freshtree.ErrUnavailable) {
		t.Fatalf("archive accepted a Fresh workspace: %v", err)
	}
	b.Workspace.Unmerged = []model.WorkspaceMergeEvidence{{Name: "api"}}
	before := append([]model.WorkspaceMergeEvidence(nil), b.Workspace.Unmerged...)
	NoteCheckpoint(context.Background(), repository.Mirrors{State: state}, b)
	if !reflect.DeepEqual(b.Workspace.Unmerged, before) {
		t.Fatal("merge observation ran on a Fresh workspace")
	}
}

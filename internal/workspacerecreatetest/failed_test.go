package workspacerecreatetest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// A create whose Fresh bootstrap failed keeps its materializing record. A
// different create on the key asks root, and replaces the record with its
// next generation only on root's proof that the allocation was retired.
func TestACreateOnAFailedCreateWaitsForRootsRetirement(t *testing.T) {
	f := newFixture(t)
	fresh := f.m.Fresh
	f.m.Fresh = func(ctx context.Context, b *model.Bench) error {
		if len(f.fresh) == 0 {
			f.fresh = append(f.fresh, workspace.Clone(b))
			return errors.New("bootstrap failed while materializing")
		}
		return fresh(ctx, b)
	}
	if _, err := f.m.Stage(context.Background(), "request1", f.spec); err == nil {
		t.Fatal("the failed create succeeded")
	}
	old := f.read(t)
	if old.Workspace.Phase != "materializing" || old.ProjectID != 0 || !workspacepolicy.Unfinished(old) {
		t.Fatalf("failed record: %+v", old.Workspace)
	}
	var asked []*model.Bench
	proven := map[string]bool{}
	f.m.Retired = func(_ context.Context, b *model.Bench) error {
		asked = append(asked, workspace.Clone(b))
		if !proven[b.Workspace.ID] {
			return errors.New("root: the allocation is not retired")
		}
		return nil
	}
	refused := func(name string, m workspace.Manager, request string) {
		t.Helper()
		_, err := m.Stage(context.Background(), request, f.spec)
		if err == nil || name != "legacy" && (!errors.Is(err, workspacepolicy.ErrUnretired) || !strings.Contains(err.Error(), "conflicts with an existing bench")) {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(f.read(t), old) || len(f.fresh) != 1 {
			t.Fatalf("%s changed the record or reached the helper", name)
		}
	}
	// Without a proof route, and while root has not retired it, the record stands.
	noProof := f.m
	noProof.Retired = nil
	refused("no proof route", noProof, "request2")
	refused("not retired", f.m, "request2")
	if len(asked) != 1 || !reflect.DeepEqual(asked[0], old) {
		t.Fatalf("root was not asked about the exact record: %d", len(asked))
	}
	legacy := f.m
	legacy.Fresh = nil
	refused("legacy", legacy, "request2")
	t.Log("FAILED_CREATE_DAEMON_REFUSED_UNTIL_RETIRED_PASS")

	// Root retired it: the next create makes the next generation.
	proven[old.Workspace.ID] = true
	second, err := f.m.Stage(context.Background(), "request2", f.spec)
	if err != nil {
		t.Fatal("next generation:", err)
	}
	w := second.Workspace
	if w.Phase != "initializing" || w.Generation != 2 || w.Previous != old.Workspace.ID || w.ID == old.Workspace.ID || w.CreationID != "request2" || second.ProjectID != 10000 {
		t.Fatalf("second generation: %+v %d", w, second.ProjectID)
	}
	if got := f.fresh[1]; len(f.fresh) != 2 || got.ProjectID != 0 || got.Workspace.Generation != 2 || got.Workspace.Previous != old.Workspace.ID || got.Workspace.ID != w.ID {
		t.Fatalf("helper input %+v", got.Workspace)
	}
	t.Log("FAILED_CREATE_DAEMON_NEXT_GENERATION_PASS")
	// The same create retries exactly. The old create and any other ask root,
	// which has not retired the live second generation: they conflict.
	asked = nil
	if retry, err := f.m.Stage(context.Background(), "request2", f.spec); err != nil || !reflect.DeepEqual(retry, second) || len(f.fresh) != 2 {
		t.Fatalf("retry: %v %d", err, len(f.fresh))
	}
	for _, request := range []string{"request1", "request3"} {
		if _, err := f.m.Stage(context.Background(), request, f.spec); err == nil || !strings.Contains(err.Error(), "conflicts with an existing bench") {
			t.Errorf("%s on the second generation: %v", request, err)
		}
	}
	if !reflect.DeepEqual(f.read(t), second) || len(f.fresh) != 2 || len(asked) != 2 || asked[0].Workspace.ID != w.ID || asked[1].Workspace.ID != w.ID {
		t.Fatalf("a refused create changed the second generation; root asked %d times", len(asked))
	}
}

package workspacerecreatetest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// refuseCapacityOnce makes the fixture's next Fresh bootstrap refuse for
// capacity, as root's reservation check does before it writes any intent.
func refuseCapacityOnce(f *fixture) {
	fresh := f.m.Fresh
	refused := false
	f.m.Fresh = func(ctx context.Context, b *model.Bench) error {
		if !refused {
			refused = true
			f.fresh = append(f.fresh, workspace.Clone(b))
			return &hoststorage.CapacityError{NeededBytes: 18253611008, AvailableBytes: 17112760320, NeededInodes: 1, AvailableInodes: 2}
		}
		return fresh(ctx, b)
	}
}

// A create refused for capacity leaves its materializing record with no
// project, and the operation's cancellation stops its initialization owner.
// Root retired nothing, because it reserved nothing, so a new create on the
// key replaces the record in its own generation once root proves it holds
// nothing of it; never on the daemon's record alone. Both a key's first
// generation and a later one recover.
func TestACapacityRefusedCreateIsReplacedOnRootsProofOfAbsence(t *testing.T) {
	for _, later := range []bool{false, true} {
		f := newFixture(t)
		generation, previous := uint32(0), ""
		if later {
			first, err := f.m.Stage(context.Background(), "request0", f.spec)
			if err != nil {
				t.Fatal(err)
			}
			f.reclaim(t)
			generation, previous = 2, first.Workspace.ID
		}
		refuseCapacityOnce(f)
		var capacity *hoststorage.CapacityError
		if _, err := f.m.Stage(context.Background(), "request1", f.spec); !errors.As(err, &capacity) {
			t.Fatalf("the refused create: %v", err)
		}
		refused := f.read(t)
		refused.Lease.StopRequested, refused.Lease.Continuation = true, ""
		f.setRecord(t, refused)
		if w := refused.Workspace; w.Phase != "materializing" || refused.ProjectID != 0 || w.Generation != generation || w.Previous != previous || !workspacepolicy.Unfinished(refused) {
			t.Fatalf("refused record: %+v %d", w, refused.ProjectID)
		}
		calls := len(f.fresh)
		var retiredAsked, absentAsked []*model.Bench
		f.m.Retired = func(_ context.Context, b *model.Bench) error {
			retiredAsked = append(retiredAsked, workspace.Clone(b))
			return errors.New("root: no intent to have retired")
		}
		proven := false
		f.m.Absent = func(_ context.Context, b *model.Bench) error {
			absentAsked = append(absentAsked, workspace.Clone(b))
			if !proven {
				return errors.New("root: the create's bootstrap evidence exists")
			}
			return nil
		}
		for name, m := range map[string]workspace.Manager{"no absence route": func() workspace.Manager { m := f.m; m.Absent = nil; return m }(), "root refuses": f.m} {
			if _, err := m.Stage(context.Background(), "request2", f.spec); !errors.Is(err, workspacepolicy.ErrUnretired) {
				t.Fatalf("%s: %v", name, err)
			}
			if !reflect.DeepEqual(f.read(t), refused) || len(f.fresh) != calls {
				t.Fatalf("%s changed the record or reached the helper", name)
			}
		}
		if len(absentAsked) != 1 || !reflect.DeepEqual(absentAsked[0], refused) || len(retiredAsked) != 2 {
			t.Fatalf("root was not asked about the exact record: %d %d", len(absentAsked), len(retiredAsked))
		}
		t.Log("CAPACITY_REFUSED_CREATE_KEPT_UNTIL_ROOT_PROVES_ABSENCE_PASS")

		proven = true
		next, err := f.m.Stage(context.Background(), "request2", f.spec)
		if err != nil {
			t.Fatal("replacement:", err)
		}
		w := next.Workspace
		if w.Phase != "initializing" || w.Generation != generation || w.Previous != previous || w.ID == refused.Workspace.ID || w.CreationID != "request2" || next.ProjectID == 0 {
			t.Fatalf("replacement: %+v %d", w, next.ProjectID)
		}
		got := f.fresh[len(f.fresh)-1]
		if len(f.fresh) != calls+1 || got.ProjectID != 0 || got.Workspace.ID != w.ID || got.Workspace.Generation != generation || got.Workspace.Previous != previous || got.Lease.StopRequested {
			t.Fatalf("helper input: %+v", got.Workspace)
		}
		if !reflect.DeepEqual(f.read(t), next) {
			t.Fatal("the replacement is not the key's record")
		}
		t.Log("CAPACITY_REFUSED_CREATE_REPLACED_IN_ITS_GENERATION_PASS")
	}
}

package workspacereclaim

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

const data = "/srv/bench-data"

// released is a released, allocated, checkpointed Fresh workspace.
func released(t *testing.T) *model.Bench {
	t.Helper()
	root := filepath.Join(data, "benches", "card")
	repo := "/srv/source.git"
	b := &model.Bench{Key: "card", Path: root, ProjectID: 10007, QuotaBytes: 4096, QuotaInodes: 128, RequestedQuotaBytes: 4096, Workspace: &model.Workspace{ID: strings.Repeat("c", 32), CreationID: "request1", CheckpointRepository: "api", Phase: "ready", ExplicitQuotaBytes: 4096,
		Preparation: []model.WorkspacePreparation{{Name: "api", Digest: strings.Repeat("1", 64)}},
		Members:     []model.WorkspaceMember{{Name: "api", Repository: repo, Branch: "bench/card", Base: "main", Path: filepath.Join(root, "api"), Mirror: repository.Path(data, repo), Source: strings.Repeat("d", 40), ObjectFormat: "sha1", RequestedQuotaBytes: 4096}}}}
	c := &model.WorkspaceCheckpoint{GenerationID: strings.Repeat("e", 32), Members: []model.WorkspaceCommit{{Name: "api", Commit: strings.Repeat("d", 40)}}}
	m, err := workspacepolicy.Manifest(b, c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	c.Digest, c.Seal = checkpointset.Digest(raw), strings.Repeat("f", 40)
	b.Workspace.Checkpoint = c
	if err = workspacepolicy.Validate(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// store is an in-memory daemon state whose updates pass the real record and
// transition validation, as the SQLite store's do.
type store struct {
	rows    benchstate.Database
	commits int
}

func (s *store) within(fn func(workspace.Update) error) error {
	return fn(func(change workspace.Change) error {
		next := benchstate.Database{}
		for k, b := range s.rows {
			next[k] = workspace.Clone(b)
		}
		before := benchstate.Capture(next)
		if err := change(next, nil); err != nil {
			return err
		}
		if err := benchstate.Validate(next); err != nil {
			return err
		}
		if err := benchstate.ValidateTransition(before, next); err != nil {
			return err
		}
		s.rows = next
		s.commits++
		return nil
	})
}

type tree struct{ freshtree.Tree }

func (tree) Root(context.Context, *model.Bench, bool) error { return nil }
func (tree) Git(context.Context, *model.Bench, string, ...string) (string, error) {
	return "", nil
}
func (tree) Inputs(context.Context, *model.Bench, string) (registeredinspection.Inputs, error) {
	return registeredinspection.Inputs{}, nil
}
func (tree) Transport(*model.Bench, string) (stageexec.PrepareTransport, error) { return nil, nil }
func (tree) Export(context.Context, *model.Bench, string, []string) ([]byte, error) {
	return nil, nil
}

type harness struct {
	s                    *store
	m                    Manager
	calls                []string
	admitErr, releaseErr error
	helperErr            []error
	specs                []payloadbootstrap.Spec
}

func newHarness(t *testing.T, b *model.Bench) *harness {
	h := &harness{s: &store{rows: benchstate.Database{"card": b}}}
	h.m = Manager{State: "/srv/state", Data: data, Within: h.s.within, Git: func(context.Context, string, ...string) (string, error) { return "", nil },
		Fresh: func(*model.Bench) (freshtree.Tree, error) { return tree{}, nil },
		Admit: func(context.Context, *sql.Tx, *model.Bench) error {
			h.calls = append(h.calls, "admit")
			return h.admitErr
		},
		Released: func(context.Context, *model.Bench) error {
			h.calls = append(h.calls, "released")
			return h.releaseErr
		},
		Helper: func(_ context.Context, b *model.Bench, s payloadbootstrap.Spec) error {
			h.calls = append(h.calls, "helper:"+b.Workspace.Phase)
			h.specs = append(h.specs, s)
			if len(h.helperErr) != 0 {
				err := h.helperErr[0]
				h.helperErr = h.helperErr[1:]
				return err
			}
			return nil
		}}
	return h
}

func TestReclaimRecordsReclaimingBeforeTheHelperThenReclaimed(t *testing.T) {
	h := newHarness(t, released(t))
	got, err := h.m.Reclaim(context.Background(), "card")
	if err != nil || got.Workspace.Phase != "reclaimed" || h.s.rows["card"].Workspace.Phase != "reclaimed" {
		t.Fatalf("reclaim: %+v %v", got, err)
	}
	if !reflect.DeepEqual(h.calls, []string{"admit", "released", "helper:reclaiming"}) || h.s.commits != 2 {
		t.Fatalf("calls %v, commits %d", h.calls, h.s.commits)
	}
	s := h.specs[0]
	if s.Key != "card" || s.ID != strings.Repeat("c", 32) || s.QuotaBytes != 4096 || len(s.Members) != 1 || s.Members[0].Source != strings.Repeat("d", 40) || s.Validate() != nil {
		t.Fatalf("spec %+v", s)
	}
	// A reclaimed workspace is returned unchanged, without the helper.
	h.calls = nil
	if again, err := h.m.Reclaim(context.Background(), "card"); err != nil || again.Workspace.Phase != "reclaimed" || len(h.calls) != 0 {
		t.Fatalf("repeat: %v %v", h.calls, err)
	}
}

// A helper failure leaves the record reclaiming, never ready again; the same
// call then runs the helper again without re-deciding readiness.
func TestReclaimIsOneWayAndResumes(t *testing.T) {
	h := newHarness(t, released(t))
	h.helperErr = []error{errors.New("effects may have occurred; retry the same reclaim")}
	if _, err := h.m.Reclaim(context.Background(), "card"); err == nil {
		t.Fatal("helper failure hidden")
	}
	if p := h.s.rows["card"].Workspace.Phase; p != "reclaiming" {
		t.Fatalf("phase after a failed helper: %s", p)
	}
	h.calls = nil
	if got, err := h.m.Reclaim(context.Background(), "card"); err != nil || got.Workspace.Phase != "reclaimed" || !reflect.DeepEqual(h.calls, []string{"helper:reclaiming"}) {
		t.Fatalf("resume: %v %v", h.calls, err)
	}
	b := workspace.Clone(h.s.rows["card"])
	b.Workspace.Phase = "ready"
	if err := benchstate.ValidateTransition(benchstate.Capture(h.s.rows), benchstate.Database{"card": b}); err == nil {
		t.Fatal("reclaimed returned to ready")
	}
}

func TestReclaimRefusesBeforeRecordingAnything(t *testing.T) {
	stopped := func(b *model.Bench) {
		b.Lease = &model.Lease{Holder: "worker", Token: strings.Repeat("a", 48), Continuation: strings.Repeat("b", 32)}
	}
	for name, c := range map[string]struct {
		change  func(*model.Bench)
		harness func(*harness)
		want    error
	}{
		"leased": {change: stopped, want: ErrConflict},
		"preparing": {change: func(b *model.Bench) {
			stopped(b)
			b.Workspace.Phase = "initializing"
			b.Lease.Holder = "initialization"
		}, want: ErrConflict},
		"legacy": {harness: func(h *harness) { h.m.Fresh = func(*model.Bench) (freshtree.Tree, error) { return nil, nil } }, want: ErrLegacy},
		"no-route": {harness: func(h *harness) {
			h.m.Fresh = func(*model.Bench) (freshtree.Tree, error) { return nil, freshtree.ErrUnavailable }
		}, want: freshtree.ErrUnavailable},
		"busy": {harness: func(h *harness) { h.admitErr = operationcontrol.ErrBusy }, want: operationcontrol.ErrBusy},
		"not-durable": {harness: func(h *harness) {
			h.releaseErr = errors.New("workspace task branch differs from its complete checkpoint")
		}},
		"no-project": {change: func(b *model.Bench) { b.ProjectID, b.QuotaBytes, b.QuotaInodes = 0, 0, 0 }, want: ErrConflict},
	} {
		t.Run(name, func(t *testing.T) {
			b := released(t)
			if c.change != nil {
				c.change(b)
			}
			h := newHarness(t, b)
			if c.harness != nil {
				c.harness(h)
			}
			before := workspace.Clone(h.s.rows["card"])
			_, err := h.m.Reclaim(context.Background(), "card")
			if err == nil || c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("not refused as %v: %v", c.want, err)
			}
			if !reflect.DeepEqual(h.s.rows["card"], before) || h.s.commits != 0 || len(h.specs) != 0 {
				t.Fatalf("a refused reclaim recorded or ran: %v %d", h.calls, h.s.commits)
			}
		})
	}
	h := newHarness(t, released(t))
	if _, err := h.m.Reclaim(context.Background(), "other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown key: %v", err)
	}
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 7, RequestID: "typed", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	scoped, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.Reclaim(scoped, "card"); !errors.Is(err, operationcontrol.ErrAuthority) || h.s.commits != 0 {
		t.Fatalf("a receipt-scoped reclaim: %v", err)
	}
}
